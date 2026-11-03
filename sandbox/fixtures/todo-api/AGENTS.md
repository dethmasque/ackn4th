# Notes for agents

- Standard library only; no third-party modules.
- Keep handlers in main.go and storage in store.go.
- Every new endpoint needs a test that uses net/http/httptest.
- Update the endpoint table in README.md when the API changes.
