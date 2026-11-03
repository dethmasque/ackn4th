package main

import (
	"errors"
	"sync"
)

// Todo is one item on the list.
type Todo struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// ErrNotFound is returned for an unknown todo ID.
var ErrNotFound = errors.New("todo not found")

// Store keeps todos in memory.
type Store struct {
	mu     sync.Mutex
	todos  []Todo
	nextID int
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{nextID: 1} }

// Add creates a todo and returns it.
func (s *Store) Add(title string) Todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Todo{ID: s.nextID, Title: title}
	s.nextID++
	s.todos = append(s.todos, t)
	return t
}

// List returns every todo, oldest first.
func (s *Store) List() []Todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Todo(nil), s.todos...)
}

// Complete marks a todo done.
func (s *Store) Complete(id int) (Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Done = true
			return s.todos[i], nil
		}
	}
	return Todo{}, ErrNotFound
}
