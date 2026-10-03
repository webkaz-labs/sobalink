// Package servicepresets supplies editable examples to the CLI and local Web UI.
package servicepresets

// Preset describes an example application's actual protocol and target port.
// LocalPort is the suggested outbound listener, which may differ for low ports.
type Preset struct {
	ID        string `json:"id"`
	Purpose   string `json:"purpose"`
	Network   string `json:"network"`
	Port      int    `json:"port"`
	LocalPort int    `json:"localPort"`
	Label     Label  `json:"label"`
}
type Label struct {
	EN string `json:"en"`
	JA string `json:"ja"`
}

var catalog = [...]Preset{
	{ID: "web", Purpose: "web", Network: "tcp", Port: 8080, LocalPort: 8080, Label: Label{EN: "Web", JA: "Web"}},
	{ID: "ssh", Purpose: "ssh", Network: "tcp", Port: 22, LocalPort: 2222, Label: Label{EN: "SSH / SFTP", JA: "SSH / SFTP"}},
	{ID: "postgres", Purpose: "postgres", Network: "tcp", Port: 5432, LocalPort: 5432, Label: Label{EN: "PostgreSQL", JA: "PostgreSQL"}},
	{ID: "local-ai", Purpose: "local-ai", Network: "tcp", Port: 11434, LocalPort: 11434, Label: Label{EN: "Local AI API", JA: "ローカルAI API"}},
}

func All() []Preset { return append([]Preset(nil), catalog[:]...) }
func Find(id string) (Preset, bool) {
	for _, item := range catalog {
		if item.ID == id {
			return item, true
		}
	}
	return Preset{}, false
}
