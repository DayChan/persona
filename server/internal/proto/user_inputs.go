package proto

// UserInputPosition is a genuine input's stable transcript identity, not its body or a model turn count.
type UserInputPosition struct {
	ID  string `json:"id"`
	Seq int    `json:"seq"`
}

// UserInputIndex is the temporary web count/position contract. The richer #2071 API supersedes it when merged.
type UserInputIndex struct {
	Total   int                 `json:"total"`
	Inputs  []UserInputPosition `json:"inputs"`
	History string              `json:"history"`
}
