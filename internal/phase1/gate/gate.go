package gate

type AttemptPlan struct {
	AttemptNumber int
	Resumed       bool
	Sidecar       *Sidecar // non-nil only when Resumed
}

// PlanAttempt decides resume vs fresh. resumeAccepted is the user's
// answer to the resume prompt when a sidecar exists. A declined or
// absent sidecar starts a fresh attempt and clears any stale sidecar.
func PlanAttempt(root, curriculumID string, resumeAccepted bool) (AttemptPlan, error) {
	sc, err := LoadSidecar(root, curriculumID)
	if err != nil {
		return AttemptPlan{}, err
	}
	if sc != nil && resumeAccepted {
		return AttemptPlan{AttemptNumber: sc.AttemptNumber, Resumed: true, Sidecar: sc}, nil
	}
	if sc != nil {
		if err := ClearSidecar(root, curriculumID); err != nil {
			return AttemptPlan{}, err
		}
	}
	n, err := NextAttemptNumber(root, curriculumID)
	if err != nil {
		return AttemptPlan{}, err
	}
	return AttemptPlan{AttemptNumber: n, Resumed: false}, nil
}

// HasInProgress reports whether a resumable sidecar exists (drives the
// CLI's resume prompt).
func HasInProgress(root, curriculumID string) (bool, error) {
	sc, err := LoadSidecar(root, curriculumID)
	return sc != nil, err
}
