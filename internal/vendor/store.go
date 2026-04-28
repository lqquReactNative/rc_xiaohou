package vendor

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("vendor not found")

// Store is an interface for vendor profile persistence.
type Store interface {
	Create(req CreateRequest) (*Profile, error)
	Get(id string) (*Profile, error)
	List() ([]*Profile, error)
	Update(id string, req UpdateRequest) (*Profile, error)
	Delete(id string) error
}

// JSONStore persists vendor profiles as a JSON file.
type JSONStore struct {
	mu       sync.RWMutex
	filepath string
	profiles map[string]*Profile
}

func NewJSONStore(filepath string) (*JSONStore, error) {
	s := &JSONStore{
		filepath: filepath,
		profiles: make(map[string]*Profile),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *JSONStore) load() error {
	data, err := os.ReadFile(s.filepath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var profiles []*Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return err
	}
	for _, p := range profiles {
		s.profiles[p.ID] = p
	}
	return nil
}

func (s *JSONStore) save() error {
	profiles := make([]*Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		profiles = append(profiles, p)
	}
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filepath, data, 0644)
}

func (s *JSONStore) Create(req CreateRequest) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	p := &Profile{
		ID:           uuid.New().String(),
		Name:         req.Name,
		TargetURL:    req.TargetURL,
		Method:       req.Method,
		AuthHeaders:  req.AuthHeaders,
		BodyTemplate: req.BodyTemplate,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if p.AuthHeaders == nil {
		p.AuthHeaders = make(map[string]string)
	}
	s.profiles[p.ID] = p
	return p, s.save()
}

func (s *JSONStore) Get(id string) (*Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.profiles[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (s *JSONStore) List() ([]*Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

func (s *JSONStore) Update(id string, req UpdateRequest) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.profiles[id]
	if !ok {
		return nil, ErrNotFound
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.TargetURL != nil {
		p.TargetURL = *req.TargetURL
	}
	if req.Method != nil {
		p.Method = *req.Method
	}
	if req.AuthHeaders != nil {
		p.AuthHeaders = req.AuthHeaders
	}
	if req.BodyTemplate != nil {
		p.BodyTemplate = *req.BodyTemplate
	}
	p.UpdatedAt = time.Now().UTC()
	return p, s.save()
}

func (s *JSONStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.profiles[id]; !ok {
		return ErrNotFound
	}
	delete(s.profiles, id)
	return s.save()
}
