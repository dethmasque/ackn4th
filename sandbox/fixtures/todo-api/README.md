# todo-api

A deliberately small JSON API for a todo list, used as an ackn4th sandbox
fixture. It has room to grow: there is no way to delete or rename a todo,
no persistence, and the HTTP handlers have no tests.

```sh
go run .            # serves on :8080
go test ./...
```

| Method | Path | Does |
| --- | --- | --- |
| GET | /todos | list todos |
| POST | /todos | create `{"title": "..."}` |
| POST | /todos/{id}/done | mark done |
