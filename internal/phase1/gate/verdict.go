package gate

// Verdict reports (allTerminal, overallPass). The attempt finalizes
// only when every component is terminal (spec §2.4); overallPass is
// meaningful only when allTerminal is true.
func Verdict(comps map[Component]ComponentOutcome, preconditionMet bool) (allTerminal bool, overallPass bool) {
	allTerminal = true
	allPass := true
	for _, c := range ComponentOrder() {
		o, ok := comps[c]
		if !ok || !o.Terminal() {
			allTerminal = false
			allPass = false
			continue
		}
		if o != OutcomePass {
			allPass = false
		}
	}
	if !allTerminal {
		return false, false
	}
	return true, allPass && preconditionMet
}
