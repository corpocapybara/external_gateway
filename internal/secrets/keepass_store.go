package secrets

import (
	"fmt"
	"os"
	"strings"

	gkl "github.com/tobischo/gokeepasslib/v3"
)

type KeePassStore struct {
	Path     string
	Password string
	db       *gkl.Database
}

func (s *KeePassStore) open() (*gkl.Database, error) {
	if s.db != nil {
		return s.db, nil
	}
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("opening keepass database: %w", err)
	}
	defer f.Close()
	db := gkl.NewDatabase()
	db.Credentials = gkl.NewPasswordCredentials(s.Password)
	if err := gkl.NewDecoder(f).Decode(db); err != nil {
		return nil, fmt.Errorf("decoding keepass database: %w", err)
	}
	if err := db.UnlockProtectedEntries(); err != nil {
		return nil, fmt.Errorf("unlocking keepass entries: %w", err)
	}
	s.db = db
	return db, nil
}

func (s *KeePassStore) findEntry(groups []gkl.Group, title string) *gkl.Entry {
	for i := range groups {
		for j := range groups[i].Entries {
			e := &groups[i].Entries[j]
			if strings.EqualFold(e.GetTitle(), title) {
				return e
			}
		}
		if e := s.findEntry(groups[i].Groups, title); e != nil {
			return e
		}
	}
	return nil
}

func (s *KeePassStore) Resolve(workspace, name string) ([]byte, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("egw/%s/%s", workspace, name)
	e := s.findEntry(db.Content.Root.Groups, title)
	if e == nil {
		return nil, fmt.Errorf("keepass entry not found: %s", title)
	}
	password := e.GetPassword()
	if password == "" {
		return nil, fmt.Errorf("keepass entry %s has empty password", title)
	}
	return []byte(password), nil
}

func (s *KeePassStore) Set(workspace, name string, value []byte) error {
	return fmt.Errorf("keepass set not implemented — manage entries directly in your KeePass client")
}

func (s *KeePassStore) Delete(workspace, name string) error {
	return fmt.Errorf("keepass delete not implemented — manage entries directly in your KeePass client")
}
