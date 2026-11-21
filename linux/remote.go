package main

import (
	"fmt"
	"strings"

	"daybreaknvr/internal/engine"
)

// remoteViewerURL is the published Vercel viewer for remote
// (away-from-home) viewing. Filled in after the v1.2 deployment.
const remoteViewerURL = "https://daybreak-nvr-daybreakdata-3224s-projects.vercel.app"

// startRemote launches the on-demand remote-viewing manager.
func (n *nvrApp) startRemote() {
	n.remote = engine.NewRemoteManager(n.ffmpeg, func() engine.Settings {
		return n.settings
	})
	n.remote.Start()
}

// remoteStatusText renders the manager snapshot for the settings
// card.
func remoteStatusText(st engine.RemoteStatus) string {
	switch st.Mode {
	case engine.RemoteModeOff:
		return "Remote viewing is off."
	case "":
		return "Starting…"
	}
	var b strings.Builder
	if st.Connected {
		b.WriteString("Control channel: connected")
	} else if st.LastError != "" {
		b.WriteString("Control channel: can't reach the relay (" + st.LastError + ")")
	} else {
		b.WriteString("Control channel: not connected")
	}
	if !st.LastPoll.IsZero() {
		fmt.Fprintf(&b, " · last check %s", st.LastPoll.Format("3:04:05 PM"))
	}
	if len(st.Pushing) > 0 {
		fmt.Fprintf(&b, "\nPushing live now: %s", strings.Join(st.Pushing, ", "))
	} else if st.Mode == engine.RemoteModeOnDemand {
		if st.ClipsPosted > 0 {
			fmt.Fprintf(&b, "\nRemote playback: %d clips listed, %d sent to the relay", st.ClipsPosted, st.ClipUploads)
		}
		b.WriteString("\nNothing is streaming out right now — a camera starts when someone opens the viewer.")
	} else {
		b.WriteString("\nPushers starting…")
	}
	return b.String()
}
