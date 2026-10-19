package ai

import "testing"

func TestPickTTS(t *testing.T) {
	ids := []string{"gemini-3-flash", "gemini-2.5-pro-preview-tts", "gemini-2.5-flash-preview-tts", "gemini-3-flash-tts", "gemini-3.8-live"}
	if got := pickTTS(ids); got != "gemini-3-flash-tts" {
		t.Fatal(got)
	}
	if pickTTS([]string{"gemini-3-flash"}) != "" {
		t.Fatal("no speech model")
	}
}
