package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const maxConcurrentWrites = 5

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

	DeletedAt *time.Time `json:"-"`
}

type Storage struct {
	mem []*Book
	mu  sync.RWMutex
}

type CreateRequest struct {
	Title  string `json:"title"`
	Author string `json:"author"`
}

type ReadManyRequest struct {
	IDs []int `json:"ids"`
}

type UpdateRequest struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

// ValidationError imposes a guarantee on the callee: the error
// message must be safe to show to the client.
type ValidationError struct {
	SafeMessage string
}

type Mux struct {
	*http.ServeMux

	rateLimiter chan struct{}

	logger  *slog.Logger
	service *Service
}

type Service struct {
	storage *Storage
}

func (err *ValidationError) Error() string { return err.SafeMessage }

// UserInputValid always returns a ValidationError
func (r *CreateRequest) UserInputValid() error {
	if r.Title == "" {
		return &ValidationError{"title is empty"}
	}
	if len(r.Title) > maxLenTitle {
		return &ValidationError{"title is too long"}
	}

	if r.Author == "" {
		return &ValidationError{"author is empty"}
	}
	if len(r.Author) > maxLenAuthor {
		return &ValidationError{"author is too long"}
	}

	return nil
}

// Book returns an error if UserInputValid fails. In this case,
// the error is guaranteed to be a ValidationError.
func (r *CreateRequest) Book() (*Book, error) {
	if err := r.UserInputValid(); err != nil {
		return nil, err
	}

	return &Book{
		Title:  r.Title,
		Author: r.Author,
	}, nil
}

// UserInputValid always returns a ValidationError
func (r *UpdateRequest) UserInputValid() error {
	if r.Title == "" {
		return &ValidationError{"title is empty"}
	}
	if len(r.Title) > maxLenTitle {
		return &ValidationError{"title is too long"}
	}

	if r.Author == "" {
		return &ValidationError{"author is empty"}
	}
	if len(r.Author) > maxLenAuthor {
		return &ValidationError{"author is too long"}
	}

	if r.ID < 0 {
		return &ValidationError{fmt.Sprintf("negative ID is not allowed: %v", r.ID)}
	}
	if r.ID == 0 {
		return &ValidationError{"ID must be set"}
	}

	return nil
}

// Book returns an error if UserInputValid fails. In this case,
// the error is guaranteed to be a ValidationError.
func (r *UpdateRequest) Book() (*Book, error) {
	if err := r.UserInputValid(); err != nil {
		return nil, err
	}

	return &Book{
		ID:     r.ID,
		Title:  r.Title,
		Author: r.Author,
	}, nil
}

func NewMux(rateLimit int, l *slog.Logger, s *Service) *Mux {
	m := &Mux{}

	if rateLimit <= 0 {
		panic(fmt.Sprintf("NewMux: invalid rateLimit: %v", rateLimit))
	}
	m.rateLimiter = make(chan struct{}, rateLimit)

	m.logger = l
	m.service = s

	m.ServeMux = http.NewServeMux()

	m.ServeMux.HandleFunc("POST /books", m.Create)
	m.ServeMux.HandleFunc("POST /books/search", m.ReadMany)
	m.ServeMux.HandleFunc("GET /books/{id}", m.Read)
	m.ServeMux.HandleFunc("PUT /books", m.Update)
	m.ServeMux.HandleFunc("DELETE /books/{id}", m.Delete)

	return m
}

func NewService(storage *Storage) *Service {
	s := &Service{}
	s.storage = storage

	return s
}

func NewStorage() *Storage {
	s := &Storage{}

	return s
}

func (m *Mux) Create(w http.ResponseWriter, r *http.Request) {
	select {
	case m.rateLimiter <- struct{}{}:
		defer func() { <-m.rateLimiter }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, "too many requests\n")
		return
	}

	var cr CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&cr); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "cannot parse book\n")
		return
	}

	b, err := cr.Book()
	switch {
	case errors.As(err, new(*ValidationError)):
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "input is invalid: %v\n", err)
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to validate book\n")
		m.logger.Error("failed to validate book", "error", err)
		return
	}

	newbook, err := m.service.Create(r.Context(), b)
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to create an entity\n")
		m.logger.Error("unexpected error", "error", err)
		return
	}

	var resp bytes.Buffer
	if err := json.NewEncoder(&resp).Encode(newbook); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to encode response\n")
		m.logger.Error("failed to encode response", "error", err)
		return
	}

	if written, err := resp.WriteTo(w); err != nil {
		m.logger.Warn("failed to write to write to client", "written", written, "error", err)
	}
}

func (m *Mux) Read(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid ID: %v\n", err)
		return
	}

	b, err := m.service.Read(r.Context(), id)
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to found an entity\n")
		m.logger.Error("unexpected error", "error", err)
		return
	}

	if b != nil {
		var resp bytes.Buffer
		if err := json.NewEncoder(&resp).Encode(b); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "failed to encode response\n")
			m.logger.Error("failed to encode response", "error", err)
			return
		}

		if written, err := resp.WriteTo(w); err != nil {
			m.logger.Warn("failed to write to write to client", "written", written, "error", err)
		}
	} else {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "not found\n")
	}
}

func (m *Mux) ReadMany(w http.ResponseWriter, r *http.Request) {
	var rmr ReadManyRequest
	if err := json.NewDecoder(r.Body).Decode(&rmr); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "cannot parse request\n")
		return
	}

	books, err := m.service.ReadMany(r.Context(), rmr.IDs)
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to found an entity\n")
		m.logger.Error("unexpected error", "error", err)
		return
	}

	var resp bytes.Buffer
	if err := json.NewEncoder(&resp).Encode(map[string]any{"books": books}); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to encode response\n")
		m.logger.Error("failed to encode response", "error", err)
		return
	}

	if written, err := resp.WriteTo(w); err != nil {
		m.logger.Warn("failed to write to write to client", "written", written, "error", err)
	}
}

func (m *Mux) Update(w http.ResponseWriter, r *http.Request) {
	select {
	case m.rateLimiter <- struct{}{}:
		defer func() { <-m.rateLimiter }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, "too many requests\n")
		return
	}

	var ur UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&ur); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "cannot parse book\n")
		return
	}

	b, err := ur.Book()
	switch {
	case errors.As(err, new(*ValidationError)):
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "input is invalid: %v\n", err)
		return
	case err != nil:
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to validate book\n")
		m.logger.Error("failed to validate book", "error", err)
		return
	}

	found, err := m.service.Update(r.Context(), b)
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
		m.logger.Error("unexpected error", "error", err)
		return
	}

	var resp bytes.Buffer
	if err := json.NewEncoder(&resp).Encode(found); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to encode response\n")
		m.logger.Error("failed to encode response", "error", err)
		return
	}

	if written, err := resp.WriteTo(w); err != nil {
		m.logger.Warn("failed to write to write to client", "written", written, "error", err)
	}
}

func (m *Mux) Delete(w http.ResponseWriter, r *http.Request) {
	select {
	case m.rateLimiter <- struct{}{}:
		defer func() { <-m.rateLimiter }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, "too many requests\n")
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "invalid ID: %v\n", err)
		return
	}

	err = m.service.Delete(r.Context(), id)
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
		m.logger.Error("unexpected error", "error", err)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "ok\n")
}

func (s *Service) Create(ctx context.Context, b *Book) (*Book, error) {
	return withContext(ctx, func() (*Book, error) { return s.storage.Create(b), nil })
}

func (s *Service) Read(ctx context.Context, id int) (*Book, error) {
	return withContext(ctx, func() (*Book, error) { return s.storage.Read(id), nil })
}

func (s *Service) ReadMany(ctx context.Context, ids []int) ([]*Book, error) {
	books := make([]*Book, 0, len(ids))

	for _, id := range ids {
		if b := s.storage.Read(id); b != nil {
			books = append(books, b)
		}
	}

	return books, nil
}

func (s *Service) Update(ctx context.Context, r *Book) (*Book, error) {
	return withContext(ctx, func() (*Book, error) { return s.storage.Update(r) })
}

func (s *Service) Delete(ctx context.Context, id int) error {
	_, err := withContext(ctx, func() (struct{}, error) { return struct{}{}, s.storage.Delete(id) })
	return err
}

func (s *Storage) NonDeleted() iter.Seq[*Book] {
	return func(yield func(*Book) bool) {
		for _, b := range s.mem {
			if b != nil && b.DeletedAt == nil {
				if !yield(b) {
					return
				}
			}
		}
	}
}

func (s *Storage) Create(b *Book) *Book {
	newbook := new(*b)

	s.mu.Lock()
	defer s.mu.Unlock()

	id := len(s.mem) + 1
	newbook.ID = id
	s.mem = append(s.mem, newbook)

	return newbook
}

func (s *Storage) Read(id int) *Book {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.byID(id)
}

func (s *Storage) Update(r *Book) (*Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.byID(r.ID)
	if b == nil {
		return nil, errNotFound
	}

	b.Title = r.Title
	b.Author = r.Author

	return b, nil
}

func (s *Storage) Delete(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.byID(id)
	if b == nil {
		return errNotFound
	}

	b.DeletedAt = new(time.Now())

	return nil
}

func (s *Storage) byID(id int) *Book {
	found := sort.Search(
		len(s.mem),
		func(i int) bool {
			return s.mem[i].ID >= id
		},
	)

	if found < len(s.mem) && s.mem[found].ID == id && s.mem[found].DeletedAt == nil {
		return s.mem[found]
	} else {
		return nil
	}
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

func main() {
	logger := slog.New(
		slog.NewJSONHandler(
			os.Stderr,
			&slog.HandlerOptions{
				AddSource: true,
			},
		),
	)

	storage := NewStorage()

	service := NewService(storage)

	mux := NewMux(maxConcurrentWrites, logger, service)

	listener, err := net.Listen("tcp", ":8090")
	if err != nil {
		logger.Error("failed to acquire a listener", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Handler: mux,
	}

	shutdownerr := make(chan error, 1)

	sigctx, sigstop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer sigstop()

	go func() {
		<-sigctx.Done()

		logger.Info("shutdown initiated")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			shutdownerr <- fmt.Errorf("run: failed to shutdown server: %w\n", err)
		} else {
			shutdownerr <- nil
		}
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		logger.Error("http handler failed", "error", err)
		os.Exit(1)
	}

	if err := <-shutdownerr; err != nil {
		logger.Error("failed to shutdown server", "error", err)
		os.Exit(1)
	}
}
