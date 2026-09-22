package core

import "context"

// Recheck runs one monitoring pass immediately, outside the ticker.
//
// WINDOWS_CLIENT.md 29 requires the client to verify the connection after
// the machine resumes: a tunnel that was up before the lid closed is not
// necessarily up after it opens, and waiting up to a whole monitoring
// interval to find out is a person staring at a connected icon that is
// lying to them. A forced tick is exactly the verification the document
// asks for -- the prober runs, and a tunnel that did not survive is judged
// through the path that already exists, retry and recovery included.
//
// It is safe to call from anywhere at any time: Tick takes the manager's
// operation lock, and does nothing unless a connection is being monitored.
func (a *Application) Recheck(ctx context.Context) {
	a.manager.Tick(ctx, a.now())
}
