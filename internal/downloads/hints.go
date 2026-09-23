package downloads

import (
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// Hint is a suggestion for getting a problem download working.
type Hint struct {
	Title    string
	Text     string
	Steps    []string
	Commands []string // shown as copyable code
	Links    []Link
}

type Link struct{ Label, URL string }

// Suggest looks at what went wrong with a download (its error, plus the
// warnings in its log, e.g. "gated" from the registry check) and returns
// suggestions, most likely first. ollamaVersion is included in the update
// hint when known.
func Suggest(model, errText string, warnings []string, ollamaVersion string) []Hint {
	text := strings.ToLower(errText + "\n" + strings.Join(warnings, "\n"))
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(text, s) {
				return true
			}
		}
		return false
	}
	ref, _ := ollama.ParseName(model)
	isHF := ref.Host == "hf.co"

	var hints []Hint
	switch {
	// Hugging Face moved model files to new storage (Xet) behind a CDN on a
	// different host; older Ollama refuses to follow the redirect. Models
	// using a newer architecture get an explicit "newer version" error.
	case has("blocked redirect to a different host", "newer version of ollama", "unknown model architecture", "unsupported model"):
		hints = append(hints, updateOllamaHint(ollamaVersion))
	}
	if isHF && has("gated", "private", "401", "403", "unauthorized", "forbidden", "access to model", "restricted") {
		hints = append(hints, hfAccessHint(ref))
	}
	if has("file does not exist", "manifest unknown", "not found") && !has("gated", "private") {
		hints = append(hints, notFoundHint(ref, isHF))
	}
	if has("no space left on device", "disk full") {
		hints = append(hints, Hint{
			Title: "Out of disk space",
			Text:  "The disk Ollama stores models on is full.",
			Steps: []string{"Delete models you no longer use (the Models page can sort by size), or free up space on that disk, then retry."},
			Links: []Link{{"Models, largest first", "/models?dir=desc&sort=size"}},
		})
	}
	if has("contact ollama at") {
		hints = append(hints, Hint{
			Title: "Couldn't reach Ollama",
			Text:  "This app couldn't connect to the Ollama server, so the download never started (or was cut off).",
			Steps: []string{
				"Check Ollama is running, e.g. open its /api/version URL in a browser.",
				"Check this app's OLLAMA_HOST setting points at it. From inside Docker, use host.docker.internal rather than localhost.",
				"Retry once it's back. Anything already downloaded is kept.",
			},
		})
	} else if has("no such host", "i/o timeout", "connection reset", "connection refused", "tls handshake", "network is unreachable", "unexpected eof", "max retries", "no progress from ollama", "temporary failure in name resolution") {
		hints = append(hints, Hint{
			Title: "Network problem",
			Text:  "Ollama does the downloading, so this is about the network of the machine Ollama runs on, not this app's.",
			Steps: []string{
				"Check that machine can reach the internet (and DNS works).",
				"Behind a proxy? Set HTTPS_PROXY in Ollama's environment, not this app's.",
				"Retry: Ollama keeps what it has already downloaded, so it picks up where it stopped.",
			},
		})
	}
	if len(hints) == 0 && strings.TrimSpace(errText) != "" {
		hints = append(hints, Hint{
			Title: "Not sure what went wrong",
			Text:  "This error isn't one the app recognises.",
			Steps: []string{
				"Retry: temporary registry and network problems are common, and progress is kept.",
				"Check Ollama's own log for more detail (the output of `ollama serve`, `journalctl -u ollama`, or `docker logs` for an Ollama container).",
				"Try `ollama pull` with the same name on the Ollama machine to see if it fails the same way.",
			},
		})
	}
	return hints
}

func updateOllamaHint(version string) Hint {
	text := "This version of Ollama can't download this model."
	if version != "" {
		text = "Your Ollama (v" + version + ") can't download this model."
	}
	return Hint{
		Title: "Update Ollama",
		Text: text + " Common causes: the model needs a newer Ollama to run, or its files are on a download host older " +
			"versions won't follow (Hugging Face's newer storage, for example, fails with \"blocked redirect to a different host\").",
		Steps: []string{
			"Install the latest Ollama from ollama.com (the desktop app also updates itself from its menu).",
			"Running Ollama in Docker? Pull the new image and recreate the container.",
			"Then retry this download.",
		},
		Commands: []string{"docker pull ollama/ollama:latest"},
		Links: []Link{
			{"Download Ollama", "https://ollama.com/download"},
			{"Ollama releases", "https://github.com/ollama/ollama/releases"},
		},
	}
}

func hfAccessHint(ref ollama.Ref) Hint {
	repo := "https://huggingface.co/" + ref.Namespace + "/" + ref.Model
	return Hint{
		Title: "Gated or private Hugging Face model",
		Text:  "This repo needs you to have access, and the machine running Ollama to be signed in to Hugging Face, before it can be pulled.",
		Steps: []string{
			"Open the model's page on Hugging Face, sign in, and accept its terms or request access. Gated models can take a while to be approved.",
			"On the machine running Ollama, install the Hugging Face CLI and log in with an access token (create one under Settings → Access Tokens).",
			"Add Ollama's public key to your Hugging Face account under Settings → SSH and GPG Keys. Ollama signs its requests to Hugging Face with it.",
			"Retry this download.",
		},
		Commands: []string{
			`pip install -U "huggingface_hub[cli]"`,
			"hf auth login",
			"cat ~/.ollama/id_ed25519.pub",
		},
		Links: []Link{
			{"Model page", repo},
			{"Access tokens", "https://huggingface.co/settings/tokens"},
			{"SSH keys", "https://huggingface.co/settings/keys"},
			{"Hugging Face CLI docs", "https://huggingface.co/docs/huggingface_hub/en/guides/cli"},
			{"Using Ollama with Hugging Face", "https://huggingface.co/docs/hub/en/ollama"},
		},
	}
}

func notFoundHint(ref ollama.Ref, isHF bool) Hint {
	h := Hint{
		Title: "Model or tag not found",
		Text:  "The registry doesn't have this name, or doesn't have this tag of it.",
		Steps: []string{"Check the spelling and the tag, then use Retry to correct the name."},
	}
	switch {
	case isHF:
		h.Steps = append(h.Steps, "For Hugging Face GGUF repos the tag is the quantisation, e.g. :Q4_K_M. It has to match one of the .gguf files in the repo.")
		h.Links = []Link{{"Repo files", "https://huggingface.co/" + ref.Namespace + "/" + ref.Model + "/tree/main"}}
	case ref.Host == "registry.ollama.ai" && ref.Namespace == "library":
		h.Links = []Link{{"Available tags", "https://ollama.com/library/" + ref.Model + "/tags"}, {"Search models", "https://ollama.com/search"}}
	case ref.Host == "registry.ollama.ai":
		h.Links = []Link{{"Available tags", "https://ollama.com/" + ref.Namespace + "/" + ref.Model + "/tags"}}
	}
	return h
}
