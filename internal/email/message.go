package email

import "encoding/json"

type Address struct {
	Address string
	Name    string
}

func (a Address) MarshalJSON() ([]byte, error) {
	if a.Name == "" {
		return json.Marshal(a.Address)
	}
	return json.Marshal(struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	}{
		Address: a.Address,
		Name:    a.Name,
	})
}

type Attachment struct {
	Content     string `json:"content"`
	Filename    string `json:"filename"`
	Type        string `json:"type"`
	Disposition string `json:"disposition"`
	ContentID   string `json:"content_id,omitempty"`
}

type Message struct {
	From        Address
	ReplyTo     *Address
	To          []string
	CC          []string
	BCC         []string
	Subject     string
	Text        string
	HTML        string
	Headers     map[string]string
	Attachments []Attachment
	RawSize     int
}
