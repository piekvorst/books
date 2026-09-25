# books

## CRUD

- [ ] Correctness: protect against concurrent access

- [ ] Correctness: validate input

    Pay attention to unambiguous error messages.

- [ ] Correctness: eliminate writing a status after Write

- [ ] Concurrency safety: implement shared rate limiting for write
methods

- [ ] Concurrency safety: graceful shutdown

    Account for non-shutdown errors in http.Server.Serve

- [ ] Security: don't show the client why JSON encoding fails
(this error is entirely server-side)

- [ ] Modeling: deleted_at instead of nil

- [ ] Modeling: ID generation should survive a "compact the slice"
change (future-proofing)

- [ ] Clean structure: decouple finding from read/update/delete

- [ ] Clean structure: net/http, storage, log, and others: remove the global state

- [ ] Clean structure: decouple storage from HTTP handling

    Pay attention to concurrent access: the HTTP handler shouldn't
    have to defend the data it receives from races. In other words,
    the data it receives should be guaranteed to be immutable.

    There are two ways to approach this problem. One is to return a
    pointer to immutable data. The other is to return a copy. Either
    approach is fine.

- [ ] Clean structure: decouple service from HTTP handling

- [ ] Concurrency safety: comply with the request's context

    Make the service context-aware but leave the storage as is. Make
    the service comply with the context without changing the storage's
    contract.

    Since the storage operations cannot be canceled, detach from them
    instead. Document the effects on the shared state.

- [ ] Clean structure: decouple storage

- [ ] Observability: use slog

- [ ] Performance: make searching O(log n) time

- [ ] Clean structure: decouple common HTTP handling into
middleware

    - [ ] rate limiting

    - [ ] logging attrs (method, path, injected via context)

    - [ ] panic recovery

## Implement /books/search

- [ ] Naive working implementation

- [ ] The output order should match the input order, which is
assumed to be arbitrary.

    - Return 400 on input with a duplicate ID.

    - If an element is not found or deleted, it should be null.

- [ ] Request each entity in a distinct goroutine

- [ ] Bound the no. of goroutines

- [ ] Make it context-aware

- [ ] All-or-nothing error handling: if at least one error is
encountered, fail. Use a probability of 0.2 to simulate an error.

    - [ ] Inject the failure function via `func() error`

## Implement /books/all

- [ ] Naive working implementation

- [ ] Naive streaming implementation

    - Do not snapshot

    - NDJSON

    - Use iter

    - Sleep for 1s between storage locks to simulate high load

- [ ] Correctness: protect against concurrent access

    - [ ] Make sure the streaming process doesn't block other methods

- [ ] Correctness: comply with the request's context
