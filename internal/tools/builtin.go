package tools

// BuiltIn is the tool set a coding agent is given: the seven Pi ships.
//
// Assembled here rather than in each composition root, so a root cannot offer
// four of them and silently lack the rest — and so that adding one reaches
// every caller at once.
//
// Root is both the directory relative paths resolve against and the directory
// commands run in. One value, because a model that reads ./a.txt and then runs
// `cat a.txt` is talking about the same file, and two roots would make those
// disagree.
func BuiltIn(root string) []Tool {
	return []Tool{
		&Bash{Dir: root},
		&Edit{Root: root},
		&Find{Root: root},
		&Grep{Root: root},
		&Ls{Root: root},
		&Read{Root: root},
		&Write{Root: root},
	}
}

// NewBuiltInRegistry returns a registry holding the built-in set.
//
// Registration validates every declared schema, so a malformed one fails here
// rather than once a model has been told it can use the tool.
func NewBuiltInRegistry(root string) (*Registry, error) {
	r := NewRegistry()
	for _, t := range BuiltIn(root) {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}
