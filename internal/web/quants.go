package web

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Quantisation names, as Hugging Face files and ollama.com tags use them
// ("Q4_K_M", "IQ3_XXS", "UD-Q4_K_XL", "BF16", "27b-it-q4_K_M"), explained in
// a line for Discover's tables. The quant_guide modal has the longer story.

// quantPartRE finds a quantisation at the end of a tag ("27b-it-q4_K_M",
// "70b-instruct-fp16", "Q5_K_P"), with Unsloth's "UD-" prefix if it has one.
var quantPartRE = regexp.MustCompile(`(?i)(?:^|[-_:])((?:ud-)?(?:i?q\d(?:_[a-z0-9]+)*|tq\d_\d|b?f(?:p)?(?:16|32)|mxfp4(?:_moe)?))$`)

// quantOf is the quantisation a tag or file is named for, or "" if its name
// doesn't say (ollama.com's "8b" or "latest": those are its default, usually
// Q4_K_M, but the page doesn't say so). QAT tags ("4b-it-qat") count too.
func quantOf(name string) string {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	if m := quantPartRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if strings.HasSuffix(strings.ToLower(name), "-qat") || strings.EqualFold(name, "qat") {
		return "qat"
	}
	return ""
}

// quantSizes are what the size letters after a k-quant or i-quant mean.
var quantSizes = map[string]string{
	"xxs": "extra extra small", "xs": "extra small", "s": "small", "m": "medium", "l": "large", "xl": "extra large",
	"nl": "non-linear",
}

// describeQuant explains a quantisation in a line, e.g. "4-bit k-quant,
// medium: the usual balance of size and quality". "" for a name it can't
// read at all.
func describeQuant(q string) string {
	lower := strings.ToLower(q)
	dynamic := strings.HasPrefix(lower, "ud-")
	lower = strings.TrimPrefix(lower, "ud-")
	var line string
	switch {
	case lower == "":
		return ""
	case lower == "qat":
		return "Quantisation-aware trained: trained to run at 4 bits, so it loses less than an ordinary 4-bit quant"
	case lower == "f32" || lower == "fp32":
		return "32-bit, unquantised: the original weights, about 8× the size of a 4-bit quant. Rarely worth running"
	case lower == "f16" || lower == "fp16" || lower == "bf16" || lower == "bfp16":
		line = "16-bit, unquantised: full quality, but about 4× the size of a 4-bit quant"
	case strings.HasPrefix(lower, "mxfp4"):
		line = "4-bit floating point (MXFP4): the format some models, such as gpt-oss, are released in"
	case strings.HasPrefix(lower, "tq"):
		line = "Ternary: only for models trained to run that way (such as BitNet)"
	default:
		line = describeIntQuant(lower)
		if line == "" {
			return ""
		}
	}
	if dynamic {
		line += ". Unsloth Dynamic: key layers kept more precise, so better than a plain quant this size"
	}
	return line
}

// describeIntQuant explains Q and IQ quants ("q4_k_m", "q8_0", "iq3_xxs").
func describeIntQuant(q string) string {
	iq := strings.HasPrefix(q, "iq")
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(q, "i"), "q"), "_")
	bits, err := strconv.Atoi(parts[0])
	if err != nil || bits < 1 || bits > 8 {
		return ""
	}
	rest := parts[1:]

	var kind, size, extra string
	switch {
	case iq:
		kind = "i-quant"
		if len(rest) > 0 {
			size, extra = sizeWord(rest[0], "IQ"+parts[0])
		}
	case len(rest) > 0 && rest[0] == "k":
		kind = "k-quant"
		if len(rest) > 1 {
			size, extra = sizeWord(rest[1], "Q"+parts[0]+"_K")
		}
	case len(rest) > 0 && (rest[0] == "0" || rest[0] == "1"):
		// Q8_0 is the usual 8-bit format; below that, k-quants have taken over.
		kind = "legacy quant"
		if bits == 8 {
			kind = "quant"
		}
		if len(rest) > 1 { // Q4_0_4_4 and the like
			extra = "Repacked for ARM CPUs, to run faster on them"
		}
	default:
		return ""
	}

	line := fmt.Sprintf("%d-bit %s", bits, kind)
	if size != "" {
		line += ", " + size
	}
	line += ": " + quantVerdict(bits, kind, size)
	if extra != "" {
		line += ". " + extra
	}
	return line
}

// sizeWord reads a k-quant's or i-quant's size letters ("m", "xxs"). Ones it
// doesn't know (Q5_K_P) are the uploader's own variant, said as such.
func sizeWord(s, base string) (size, extra string) {
	if w, ok := quantSizes[s]; ok {
		return w, ""
	}
	return "", fmt.Sprintf("“_%s” is this uploader's own variant of %s, not a standard llama.cpp size", strings.ToUpper(s), base)
}

// quantVerdict is what a quant of so many bits is like to use.
func quantVerdict(bits int, kind, size string) string {
	switch {
	case bits >= 8:
		return "practically the same as the original, at about twice the size of 4-bit"
	case bits == 6:
		return "very close to the original"
	case bits == 5:
		return "close to the original, for a little more size than 4-bit"
	case bits == 4 && kind == "k-quant" && size == "medium":
		return "the usual balance of size and quality (Ollama's default)"
	case bits == 4 && kind == "legacy quant":
		return "an older method; a 4-bit k-quant of the same size is usually better"
	case bits == 4 && kind == "i-quant":
		return "about as good as a 4-bit k-quant, a little smaller; can be slower on a CPU"
	case bits == 4:
		return "a good balance of size and quality"
	case bits == 3:
		return "noticeably less accurate; for fitting a bigger model in"
	}
	return "a big loss in quality; a last resort for fitting a model in"
}
