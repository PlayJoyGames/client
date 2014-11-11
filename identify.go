package libkb

import (
	"fmt"
	"strings"
	"sync"
)

func (u *User) IdentifyKey(is IdentifyState) error {
	var ds string
	if mt := is.track; mt != nil {
		diff := mt.ComputeKeyDiff(*u.activePgpFingerprint)
		is.res.KeyDiff = diff
		ds = diff.ToDisplayString() + " "
	}
	fp, e := u.GetActivePgpFingerprint()
	if e != nil {
		return e
	}
	msg := CHECK + " " + ds +
		ColorString("green", "public key fingerprint: "+fp.ToQuads())
	is.Report(msg)
	return nil
}

type IdentifyArg struct {
	ReportHook func(s string) // Can be nil
	Me         *User          // The user who's doing the tracking
}

func (i IdentifyArg) MeSet() bool {
	return i.Me != nil
}

type IdentifyRes struct {
	Error       error
	KeyDiff     TrackDiff
	ProofChecks []LinkCheckResult
	Warnings    []Warning
	Messages    []string
	MeSet       bool // whether me was set at the time
}

func (i IdentifyRes) NumProofFailures() int {
	nfails := 0
	for _, c := range i.ProofChecks {
		if c.err != nil {
			nfails++
		}
	}
	return nfails
}

func (i IdentifyRes) NumTrackFailures() int {
	ntf := 0
	for _, c := range i.ProofChecks {
		if c.diff != nil && c.diff.BreaksTracking() {
			ntf++
		}
	}
	return ntf
}

func (i IdentifyRes) GetErrorAndWarnings(strict bool) (err error, warnings Warnings) {

	if i.Error != nil {
		return i.Error, nil
	}

	probs := make([]string, 0, 0)

	if nfails := i.NumProofFailures(); nfails > 0 {
		p := fmt.Sprintf("PROBLEM: %d proof%s failed remote checks", nfails, GiveMeAnS(nfails))
		if strict {
			probs = append(probs, p)
		} else {
			warnings = []Warning{StringWarning(p)}
		}
	}

	if ntf := i.NumTrackFailures(); ntf > 0 {
		probs = append(probs,
			fmt.Sprintf("%d track copmonent%s failed",
				ntf, GiveMeAnS(ntf)))
	}

	if len(probs) > 0 {
		err = fmt.Errorf("%s", strings.Join(probs, ";"))
	}

	return
}

func (i IdentifyRes) GetError() error {
	e, _ := i.GetErrorAndWarnings(true)
	return e
}

func (i IdentifyRes) GetErrorLax() (error, Warnings) {
	return i.GetErrorAndWarnings(true)
}

func (i IdentifyState) Report(m string) {
	i.res.Messages = append(i.res.Messages, m)
	if i.arg.ReportHook != nil {
		i.arg.ReportHook(m + "\n")
	}
}

func NewIdentifyRes(m bool) *IdentifyRes {
	return &IdentifyRes{
		MeSet:       m,
		Messages:    make([]string, 0, 1),
		Warnings:    make([]Warning, 0, 0),
		ProofChecks: make([]LinkCheckResult, 0, 1),
	}
}

type IdentifyState struct {
	arg   *IdentifyArg
	res   *IdentifyRes
	u     *User
	track *TrackLookup
	mutex *sync.Mutex
}

func (s *IdentifyState) Lock() {
	s.mutex.Lock()
}

func (s *IdentifyState) Unlock() {
	s.mutex.Unlock()
}

func (res *IdentifyRes) AddLinkCheckResult(lcr LinkCheckResult) {
	res.ProofChecks = append(res.ProofChecks, lcr)
}

func NewIdentifyState(arg *IdentifyArg, res *IdentifyRes, u *User) IdentifyState {
	return IdentifyState{arg, res, u, nil, new(sync.Mutex)}
}

func (u *User) Identify(arg IdentifyArg) (res *IdentifyRes) {

	if cir := u.cachedIdentifyRes; cir != nil && (arg.MeSet() == cir.MeSet) {
		return cir
	}

	res = NewIdentifyRes(arg.MeSet())
	is := NewIdentifyState(&arg, res, u)

	if arg.Me == nil {
		// noop
	} else if tlink, err := arg.Me.GetTrackingStatementFor(u.name, u.id); err != nil {
		res.Error = err
		return
	} else if tlink != nil {
		is.track = NewTrackLookup(tlink)
		msg := ColorString("bold", fmt.Sprintf("You last tracked %s on %s",
			u.name, FormatTime(is.track.GetCTime())))
		is.Report(msg)
	}

	G.Log.Debug("+ Identify(%s)", u.name)

	if res.Error = u.IdentifyKey(is); res.Error != nil {
		return
	}
	u.IdTable.Identify(is)

	G.Log.Debug("- Identify(%s)", u.name)
	u.cachedIdentifyRes = res
	return
}

func (u *User) IdentifySimple(me *User) error {
	res := u.Identify(IdentifyArg{
		ReportHook: func(s string) { G.OutputString(s) },
		Me:         me,
	})
	return res.GetError()
}

func (u *User) IdentifySelf(bg bool) error {
	targ, err := u.GetActivePgpFingerprint()
	if err != nil {
		return err
	}
	var fp *PgpFingerprint
	if fp = G.Env.GetPgpFingerprint(); fp == nil {
		// noop
	} else if fp.Eq(*targ) {
		return nil
	} else {
		return WrongKeyError{fp, targ}
	}

	// Ok, we now need to basically "track" ourself to make sure the
	// server wasn't lying
	if bg || G.Terminal == nil {
		return NewNeedInputError("Can't verify your key fingerprint; try logging in again")
	}

	G.Log.Info("Verifying your key fingerprint....")

	ires := u.Identify(IdentifyArg{
		Me:         u,
		ReportHook: func(s string) { G.OutputString(s) },
	})

	err, warnings := ires.GetErrorLax()
	var prompt string
	if err != nil {
		return err
	} else if warnings != nil {
		warnings.Warn()
		prompt = "Do you still accept these credentials to be your own?"
	} else if len(ires.ProofChecks) == 0 {
		prompt = "We found your account, but you have no hosted proofs. Check your fingerprint carefully. Is this you?"
	} else {
		prompt = "Is this you?"
	}

	err = PromptForConfirmation(prompt)
	if err != nil {
		return err
	}

	G.Log.Warning("Setting PGP fingerprint to: %s", targ.ToQuads())
	G.Env.GetConfigWriter().SetPgpFingerprint(targ)

	return nil
}
