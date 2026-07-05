// problems, prioritized:
//
// . modeling: deleted_at instead of nil
//
// . clean structure: decouple finding from read/update/delete
//
// . clean structure: decouple repo
//
// . observability: use slog
//
// . performance: make searching O(log n) time
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	maxLenTitle  = 255
	maxLenAuthor = 255
)

var (
	errNotFound = fmt.Errorf("not found")
)

type Book struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

type Mux struct {
	*http.ServeMux

	l *log.Logger
	s *Service
}

type Service struct {
	storage []*Book
	mu      sync.RWMutex
}

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

func NewMux(l *log.Logger, s *Service) *Mux {
	m := &Mux{}
	m.l = l
	m.s = s

	m.ServeMux = http.NewServeMux()
	m.ServeMux.HandleFunc("POST /books", m.Create)
	m.ServeMux.HandleFunc("GET /books/{id}", m.Read)
	m.ServeMux.HandleFunc("PUT /books", m.Update)
	m.ServeMux.HandleFunc("DELETE /books/{id}", m.Delete)

	return m
}

func NewService() *Service {
	s := &Service{}

	return s
}

func (m *Mux) Create(w http.ResponseWriter, r *http.Request) {
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

	newbook, err := withContext(r.Context(), func() (*Book, error) { return m.s.Create(&b) })
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to create an entity\n")
		m.l.Printf("Mux.Create: unexpected error: %v\n", err)
		return
	}

	var resp bytes.Buffer
	if err := json.NewEncoder(&resp).Encode(newbook); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to encode response\n")
		m.l.Printf("Mux.Create: failed to encode response: %v\n", err)
		return
	}

	if written, err := resp.WriteTo(w); err != nil {
		m.l.Printf("Mux.Create: failed to write to write to client (written %v bytes): %v\n", written, err)
	}
}

func (m *Mux) Read(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid ID: %v\n", err)
		return
	}

	b, err := withContext(r.Context(), func() (*Book, error) { return m.s.Read(id) })
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to found an entity\n")
		m.l.Printf("Mux.Read: unexpected error: %v\n", err)
		return
	}

	if b != nil {
		var resp bytes.Buffer
		if err := json.NewEncoder(&resp).Encode(b); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "failed to encode response\n")
			m.l.Printf("Mux.Read: failed to encode response: %v\n", err)
			return
		}

		if written, err := resp.WriteTo(w); err != nil {
			m.l.Printf("Mux.Read: failed to write to write to client (written %v bytes): %v\n", written, err)
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
	}
}

func (m *Mux) Update(w http.ResponseWriter, r *http.Request) {
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

	found, err := withContext(r.Context(), func() (*Book, error) { return m.s.Update(&b) })
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, errNotFound):
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to update an entity\n")
		m.l.Printf("Mux.Update: unexpected error: %v\n", err)
		return
	}

	var resp bytes.Buffer
	if err := json.NewEncoder(&resp).Encode(found); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to encode response\n")
		m.l.Printf("Mux.Update: failed to encode response: %v\n", err)
		return
	}

	if written, err := resp.WriteTo(w); err != nil {
		m.l.Printf("Mux.Update: failed to write to write to client (written %v bytes): %v\n", written, err)
	}
}

func (m *Mux) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid ID: %v\n", err)
		return
	}

	_, err = withContext(r.Context(), func() (struct{}, error) { return struct{}{}, m.s.Delete(id) })
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, errNotFound):
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to delete an entity\n")
		m.l.Printf("Mux.Delete: unexpected error: %v\n", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "ok\n")
}

func (s *Service) Create(b *Book) (*Book, error) {
	newbook := new(*b)

	s.mu.Lock()
	defer s.mu.Unlock()

	id := len(s.storage) + 1
	newbook.ID = id
	s.storage = append(s.storage, newbook)

	return newbook, nil
}

func (s *Service) Read(id int) (*Book, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, b := range s.storage {
		if b != nil && b.ID == id {
			return b, nil
		}
	}

	return nil, nil
}

func (s *Service) Update(b *Book) (*Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.storage {
		if s.storage[i] != nil && s.storage[i].ID == b.ID {
			s.storage[i] = b
			return s.storage[i], nil
		}
	}

	return nil, errNotFound
}

func (s *Service) Delete(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.storage {
		if s.storage[i] != nil && s.storage[i].ID == id {
			s.storage[i] = nil
			return nil
		}
	}

	return errNotFound
}

func withContext[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		t   T
		err error
	}
	ch := make(chan result, 1)

	go func() {
		t, err := fn()
		ch <- result{t, err}
	}()

	select {
	case <-ctx.Done():
		return *new(T), ctx.Err()
	case r := <-ch:
		return r.t, r.err
	}
}

func sprintfInto(dst *string, format string, a ...any) {
	if dst != nil {
		*dst = fmt.Sprintf(format, a...)
	}
}

func run() error {
	logger := log.New(os.Stderr, "books: ", 0)

	service := NewService()

	mux := NewMux(logger, service)

	listener, err := net.Listen("tcp", ":8090")
	if err != nil {
		return fmt.Errorf("run: failed to acquire a listener: %w", err)
	}

	server := &http.Server{
		Handler: mux,
	}

	shutdownerr := make(chan error, 1)

	sigctx, sigstop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer sigstop()

	go func() {
		<-sigctx.Done()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			shutdownerr <- fmt.Errorf("run: failed to shutdown server: %w\n", err)
		} else {
			shutdownerr <- nil
		}
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("run: http handler failed: %w", err)
	}

	return <-shutdownerr
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "main: run failed: %v\n", err)
	}
}
