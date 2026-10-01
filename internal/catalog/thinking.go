package catalog

// ThinkingOf reports the first named catalog's thinking control for a model.
// The empty control with known=true means the source lists no thinking support;
// known=false leaves an unlisted model's behavior to its provider.
func ThinkingOf(providers []string, id string) (control string, known bool) {
	all := load()
	b := bareID(id)
	if r, ok := unprofiled(b); ok {
		b = r
	}
	for _, want := range []string{b, cutAt(b, '('), cutAt(b, ':')} {
		for _, pid := range providers {
			for key, m := range all[pid].Models {
				if bareID(key) != want {
					continue
				}
				for _, option := range m.Reasoning {
					switch option.Type {
					case "budget_tokens", "toggle", "effort":
						return option.Type, true
					}
				}
				return "", true
			}
		}
	}
	return "", false
}
