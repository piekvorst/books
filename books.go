// problems, prioritized:
//
// . correctness: eliminate writing status after Write
//
// . concurrency safety: respect request's context
//
// . concurrency safety: graceful shutdown
//
// . modeling: deleted_at instead of nil
//
// . clean structure: decouple finding from read/update/delete
//
// . clean structure: net/http, storage, and mu: remove the global state
//
// . clean structure: decouple HTTP handling
//
// . clean structure: decouple repo
//
// . observability: use slog
//
// . performance: make searching O(log n) time
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
)

const (
	maxLenTitle  = 255
	maxLenAuthor = 255
)

type Book struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

var storage []*Book

var mu sync.RWMutex

// UserInputValid validates b, assuming that the whole data is
// filled by the user.
//
// If safeError is non-nil, an error message is written there,
// guaranteeing the content to be safe to show the client.
func (b *Book) UserInputValid(safeError *string) bool {
	if b.Title == "" {
		sprintfInto(safeError, "title is empty")
		return false
	}
	if len(b.Title) > maxLenTitle {
		sprintfInto(safeError, "title is too long")
		return false
	}

	if b.Author == "" {
		sprintfInto(safeError, "author is empty")
		return false
	}
	if len(b.Author) > maxLenAuthor {
		sprintfInto(safeError, "author is too long")
		return false
	}

	return true
}

// UserInputValidForCreate validates b, applying additional rules
// assuming the input is used to create a new record.
func (b *Book) UserInputValidForCreate(safeError *string) bool {
	if !b.UserInputValid(safeError) {
		return false
	}

	if b.ID != 0 {
		sprintfInto(safeError, "ID must not be set")
		return false
	}

	return true
}

// UserInputValidForUpdate validates b, applying additional rules
// assuming the input is used to update an existing record.
func (b *Book) UserInputValidForUpdate(safeError *string) bool {
	if !b.UserInputValid(safeError) {
		return false
	}

	if b.ID < 0 {
		sprintfInto(safeError, "negative ID is not allowed: %v", b.ID)
		return false
	}
	if b.ID == 0 {
		sprintfInto(safeError, "ID must be set")
		return false
	}

	return true
}

func create(w http.ResponseWriter, r *http.Request) {
	var b Book
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "cannot parse book\n")
		return
	}

	var validationError string
	if !b.UserInputValidForCreate(&validationError) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "input is invalid: %v\n", validationError)
		return
	}

	mu.Lock()
	id := len(storage) + 1
	b.ID = id
	storage = append(storage, &b)
	mu.Unlock()

	if err := json.NewEncoder(w).Encode(b); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to encode: %v\n", err)
		return
	}
}

func read(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid ID: %v\n", err)
		return
	}

	var found *Book

	mu.RLock()
	for _, b := range storage {
		if b != nil && b.ID == id {
			found = b
			break
		}
	}
	mu.RUnlock()

	if found != nil {
		if err := json.NewEncoder(w).Encode(found); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "failed to encode: %v\n", err)
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
	}
}

func update(w http.ResponseWriter, r *http.Request) {
	var b Book
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "cannot parse book\n")
		return
	}

	var validationError string
	if !b.UserInputValidForUpdate(&validationError) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "input is invalid: %v\n", validationError)
		return
	}

	var found *Book

	mu.Lock()
	for i := range storage {
		if storage[i] != nil && storage[i].ID == b.ID {
			found = storage[i]
			break
		}
	}
	if found != nil {
		*found = b
	}
	mu.Unlock()

	if found != nil {
		if err := json.NewEncoder(w).Encode(b); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "failed to encode: %v\n", err)
			return
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
	}
}

func delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid ID: %v\n", err)
		return
	}

	var found **Book

	mu.Lock()
	for i := range storage {
		if storage[i] != nil && storage[i].ID == id {
			found = &storage[i]
			break
		}
	}
	if found != nil {
		*found = nil
	}
	mu.Unlock()

	if found != nil {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok\n")
	} else {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
	}
}

func sprintfInto(dst *string, format string, a ...any) {
	if dst != nil {
		*dst = fmt.Sprintf(format, a...)
	}
}

func main() {
	log.SetPrefix("books: ")
	log.SetFlags(0)

	http.HandleFunc("POST /books", create)
	http.HandleFunc("GET /books/{id}", read)
	http.HandleFunc("PUT /books", update)
	http.HandleFunc("DELETE /books/{id}", delete)

	log.Fatalln(http.ListenAndServe(":8090", nil))
}
