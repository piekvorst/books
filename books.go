// problems, prioritized:
//
// . correctness: validate input
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

type Book struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

var storage []*Book

var mu sync.RWMutex

func create(w http.ResponseWriter, r *http.Request) {
	var b Book
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "cannot parse book\n")
		return
	}

	if b.ID != 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "ID cannot be set\n")
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

func main() {
	log.SetPrefix("books: ")
	log.SetFlags(0)

	http.HandleFunc("POST /books", create)
	http.HandleFunc("GET /books/{id}", read)
	http.HandleFunc("PUT /books", update)
	http.HandleFunc("DELETE /books/{id}", delete)

	log.Fatalln(http.ListenAndServe(":8090", nil))
}
