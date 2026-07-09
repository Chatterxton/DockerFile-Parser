// Package compose разбирает docker-compose.yaml в нормализованную структуру.
// Задача пакета — спрятать «кривые» формы YAML (список vs карта) за чистыми
// Go-типами, чтобы пакет analyze работал с предсказуемыми полями.
package compose

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Compose — весь файл: сервисы по имени.
type Compose struct {
	Services map[string]Service `yaml:"services"`
}

// Service — один сервис с уже нормализованными полями.
type Service struct {
	Image       string         `yaml:"image"`
	Ports       StringList     `yaml:"ports"`
	DependsOn   KeyList        `yaml:"depends_on"`
	Links       []string       `yaml:"links"`
	Environment EnvMap         `yaml:"environment"`
	Networks    NetSpec        `yaml:"networks"`
	NetworkMode string         `yaml:"network_mode"`
	Extends     ExtendsSpec    `yaml:"extends"`
	Extra       map[string]any `yaml:",inline"` // прочие поля (в т.ч. развёрнутый <<-якорь)
}

// ExtendsSpec — имя сервиса, от которого наследуется этот (extends). Понимает
// строку и форму {service: name}.
type ExtendsSpec string

func (e *ExtendsSpec) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*e = ExtendsSpec(node.Value)
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "service" {
				*e = ExtendsSpec(node.Content[i+1].Value)
			}
		}
	}
	return nil
}

// NetSpec — сети сервиса: имена и сетевые алиасы (по алиасу на сервис могут
// ссылаться из env другого сервиса).
type NetSpec struct {
	Names   []string
	Aliases []string
}

// UnmarshalYAML разбирает networks в двух формах: список имён и карту
// (значение карты может содержать aliases).
func (n *NetSpec) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			n.Names = append(n.Names, item.Value)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			n.Names = append(n.Names, node.Content[i].Value)
			val := node.Content[i+1]
			if val.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(val.Content); j += 2 {
				if val.Content[j].Value == "aliases" && val.Content[j+1].Kind == yaml.SequenceNode {
					for _, a := range val.Content[j+1].Content {
						n.Aliases = append(n.Aliases, a.Value)
					}
				}
			}
		}
	}
	return nil
}

// StringList — список скаляров. В YAML порт может быть "80:80", числом 80
// или длинной формой {target: 80, published: 8080}.
type StringList []string

// KeyList — поле, которое в YAML бывает и списком строк, и картой
// (depends_on, networks). Нормализуем к списку имён.
type KeyList []string

// EnvMap — environment: список "KEY=val" или карта KEY: val.
type EnvMap map[string]string

// Parse разбирает содержимое docker-compose.yaml (заглушка).
func Parse(data []byte) (*Compose, error) {
	var c Compose
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// UnmarshalYAML: скаляр → [val]; список → элементы (скаляры как есть,
// длинная форма порта {published, target} → "published:target").
func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			switch item.Kind {
			case yaml.MappingNode:
				var m map[string]any
				if err := item.Decode(&m); err != nil {
					return err
				}
				out = append(out, longPort(m))
			default:
				out = append(out, item.Value)
			}
		}
		*s = out
	}
	return nil
}

func longPort(m map[string]any) string {
	target := fmt.Sprint(m["target"])
	if pub, ok := m["published"]; ok {
		return fmt.Sprint(pub) + ":" + target
	}
	return target
}

// UnmarshalYAML: список строк → как есть; карта → её ключи (для depends_on и
// networks в форме карты).
func (k *KeyList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			out = append(out, item.Value)
		}
		*k = out
	case yaml.MappingNode:
		out := make([]string, 0, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		*k = out
	}
	return nil
}

// UnmarshalYAML: карта KEY: val → map; список "KEY=val" → map (split по
// первому '='). Значения-числа/булевы приводятся к строке.
func (e *EnvMap) UnmarshalYAML(n *yaml.Node) error {
	out := make(map[string]string)
	switch n.Kind {
	case yaml.MappingNode:
		var raw map[string]any
		if err := n.Decode(&raw); err != nil {
			return err
		}
		for k, v := range raw {
			out[k] = fmt.Sprint(v)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			k, v, _ := strings.Cut(item.Value, "=")
			out[k] = v
		}
	}
	*e = out
	return nil
}
