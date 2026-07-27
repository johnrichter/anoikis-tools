package boundary

// Item is one dispatch's raw return, named so its outcome can be traced back to the run it
// came from.
type Item struct {
	ID  string
	Raw []byte
}

// Result is one Item's outcome: either a valid Manifest, or the named error that rejected it.
// The two are mutually exclusive — Err is nil exactly when Manifest is the decoded return.
type Result struct {
	ID       string
	Manifest Manifest
	Err      error
}

// OK reports whether r's return validated.
func (r Result) OK() bool { return r.Err == nil }

// ValidateBatch validates every item against class independently.
//
// This is the FB7 regression's fix: one item's rejection never blocks, merges into, or silently
// retries another's. A batch in which every return is rejected yields exactly len(items) named
// errors, one per item — for the caller to surface as len(items) failures, never as one
// summarized batch failure and never as a silent extra round.
func ValidateBatch(class ReturnClass, items []Item) []Result {
	out := make([]Result, len(items))
	for i, it := range items {
		m, err := Validate(class, it.Raw)
		out[i] = Result{ID: it.ID, Manifest: m, Err: err}
	}
	return out
}
