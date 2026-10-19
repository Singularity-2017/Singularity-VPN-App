package reach

// Control says whether the network answers at all, judged from the targets
// inside the caller's own country.
type Control string

const (
	Control_ok     Control = "ok"     // at least one control got a TCP connection
	Control_failed Control = "failed" // there were controls and none did
	Control_absent Control = "absent" // there were none to ask
)

// Verdict is what one target's result comes to.
type Verdict string

const (
	Verdict_open       Verdict = "open"
	Verdict_blocked    Verdict = "blocked"    // failed while the controls answered
	Verdict_no_network Verdict = "no_network" // failed and the controls failed too
	Verdict_no_answer  Verdict = "no_answer"  // failed, and that cannot be attributed
)

// Method is the first step that failed on a blocked target.
type Method string

const (
	Method_none Method = ""
	Method_dns  Method = "dns"
	Method_tcp  Method = "tcp"
	Method_tls  Method = "tls"
)

// Check is one target's result with its verdict.
type Check struct {
	Result
	Control bool // the target is a control: it has a country
	Verdict Verdict
	Method  Method // set only on Verdict_blocked
}

// Analysis is a whole run judged.
type Analysis struct {
	Control Control
	Checks  []Check
}

// Judge turns results into verdicts. It is pure.
//
// A control is a target with a country. A failure is only called a block
// when the controls prove the network answers; a control itself is never
// blocked -- it is open, or it did not answer.
func Judge(results []Result) Analysis {
	control := Control_absent
	for _, r := range results {
		if r.Target.Country == "" {
			continue
		}
		if control == Control_absent {
			control = Control_failed
		}
		if r.Tcp == Step_open {
			control = Control_ok
			break
		}
	}

	analysis := Analysis{Control: control, Checks: make([]Check, 0, len(results))}
	for _, r := range results {
		check := Check{Result: r, Control: r.Target.Country != ""}
		method := first_failed(r)
		switch {
		case method == Method_none && r.Dns == Step_open && r.Tcp == Step_open && r.Tls == Step_open:
			check.Verdict = Verdict_open
		case check.Control || method == Method_none:
			check.Verdict = Verdict_no_answer
		case control == Control_ok:
			check.Verdict, check.Method = Verdict_blocked, method
		case control == Control_failed:
			check.Verdict = Verdict_no_network
		default:
			check.Verdict = Verdict_no_answer
		}
		analysis.Checks = append(analysis.Checks, check)
	}
	return analysis
}

// first_failed is the first step, in probe order, that is blocked.
func first_failed(r Result) Method {
	switch {
	case r.Dns == Step_blocked:
		return Method_dns
	case r.Tcp == Step_blocked:
		return Method_tcp
	case r.Tls == Step_blocked:
		return Method_tls
	}
	return Method_none
}
