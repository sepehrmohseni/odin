package aggregator

// getNestedValue extracts a value from nested map using dot notation
func getNestedValue(data map[string]interface{}, path []string) (interface{}, bool) {
	current := data

	for i, key := range path {
		if i == len(path)-1 {
			value, exists := current[key]
			return value, exists
		}

		next, exists := current[key]
		if !exists {
			return nil, false
		}

		nextMap, ok := next.(map[string]interface{})
		if !ok {
			return nil, false
		}

		current = nextMap
	}

	return nil, false
}

// setNestedValue sets a value in nested map using dot notation
func setNestedValue(data map[string]interface{}, path []string, value interface{}) {
	current := data

	for i, key := range path {
		if i == len(path)-1 {
			current[key] = value
			return
		}

		next, exists := current[key]
		if !exists {
			next = make(map[string]interface{})
			current[key] = next
		}

		nextMap, ok := next.(map[string]interface{})
		if !ok {
			nextMap = make(map[string]interface{})
			current[key] = nextMap
		}

		current = nextMap
	}
}
