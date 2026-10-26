// Package update finds, verifies and stages a new version. Every channel
// is verified against the Ed25519 keys compiled into the binary, so no
// mirror has to be trusted and adding one is free (design section 6).
//
// The pieces, in the order an update passes through them:
//
//   - channels: the control plane, the mirrors named by the newest manifest
//     that verified, and a fallback mirror if the build names one -- all of
//     them asked, the newest verified manifest winning, and anything older
//     than the newest ever verified ignored as a replay. A mirror given as
//     <repository>/releases/latest/download is a code host's releases
//     (GitHub's layout), not a plain directory;
//   - the manifest and its detached signature, verified byte for byte;
//   - the setup artifact, checked against the manifest's size and SHA-256,
//     fetched first from a code host whose manifest named that same setup
//     in the same check -- its bandwidth is free, and answering is the
//     evidence it is reachable from here -- and otherwise from the control
//     plane; a download that delivers nothing for a minute moves on to the
//     next channel;
//   - a staged update, applied only when no tunnel is carrying traffic
//     unless the manifest marks it urgent or this version is below its
//     minimum -- applying means handing it to svpn-swap, which runs the
//     setup quietly, waits for the new service to prove itself healthy and
//     reverses the swap if it does not.
package update
