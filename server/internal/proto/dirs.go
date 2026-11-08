package proto

import "time"

type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsGit bool   `json:"is_git"`
}

type RecentDir struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	LastUsed time.Time `json:"last_used"`
}

type DirList struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []DirEntry  `json:"entries"`
	Recent  []RecentDir `json:"recent"`
}
