// Package recovery decides what to do when a connection fails: the retry
// budget, the cooldown and the anti-flapping window, and the switch to
// another protocol or node.
package recovery
