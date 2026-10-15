package web

// builtinTest is a ready-made model test: a prompt for one kind of job, to
// run against whichever models are picked (the Testing tab's "Built-in
// tests"). Replies are judged by reading them, so each says what a good one
// looks like.
type builtinTest struct {
	Slug    string // for ?builtin=
	Name    string
	About   string // what it checks, in a line
	Prompt  string
	Repeats int
	LookFor string // what a good reply looks like, for judging the results
}

// builtinTests are the Testing tab's ready-made tests, in the order shown.
var builtinTests = []builtinTest{
	{
		Slug: "speed", Name: "Speed check", Repeats: 5,
		About:   "A one-line reply, to compare load time and speed",
		Prompt:  "Introduce yourself in one short sentence.",
		LookFor: "Nothing to judge in the reply: compare the Load, Time and Tokens/s columns. The first run includes loading the model; the others show how quickly it answers once it's loaded.",
	},
	{
		Slug: "coding", Name: "Coding", Repeats: 3,
		About:   "A small Python function and its output",
		Prompt:  "Write a Python function fizzbuzz(n) that returns a list of strings for the numbers 1 to n: \"Fizz\" for multiples of 3, \"Buzz\" for multiples of 5, \"FizzBuzz\" for multiples of both, and the number itself otherwise. Then show what fizzbuzz(15) returns. Keep any explanation to two sentences.",
		LookFor: "Working code, and the right list for 15: 1, 2, Fizz, 4, Buzz, Fizz, 7, 8, Fizz, Buzz, 11, Fizz, 13, 14, FizzBuzz. Check it tests for 15 (or both 3 and 5) before 3 and 5 on their own, and that the explanation stays short.",
	},
	{
		Slug: "reasoning", Name: "Reasoning", Repeats: 3,
		About:   "A short puzzle with a tempting wrong answer",
		Prompt:  "A bat and a ball cost $1.10 in total. The bat costs $1.00 more than the ball. How much does the ball cost? Work it out step by step, then give the answer on its own line.",
		LookFor: "The ball costs $0.05 (and the bat $1.05). $0.10 is the tempting wrong answer. Look at whether the steps actually lead to the answer, and whether a model gets it right on every run.",
	},
	{
		Slug: "instructions", Name: "Following instructions", Repeats: 3,
		About:   "Strict rules on format and length",
		Prompt:  "Write exactly three bullet points explaining why the sky is blue. Each bullet must be fewer than 12 words. Write nothing before or after the bullets.",
		LookFor: "Exactly three bullets, each under 12 words, and nothing else: no heading, introduction or closing line. Many models add a friendly sentence anyway.",
	},
	{
		Slug: "json", Name: "Structured output (JSON)", Repeats: 3,
		About:   "Pull facts out of text as JSON",
		Prompt:  "List the people in this text as a JSON array of objects, each with \"name\" (a string) and \"age\" (a number). Reply with the JSON only, without a code block or any other text.\n\nText: Maria, who is 34, went to visit her brother Tom, 29, and their grandmother Edith, who turned 81 last week.",
		LookFor: "Valid JSON and nothing else: [{\"name\": \"Maria\", \"age\": 34}, {\"name\": \"Tom\", \"age\": 29}, {\"name\": \"Edith\", \"age\": 81}]. Ages should be numbers, not strings, and there should be no code block around it. That matters if you'll feed replies to a program.",
	},
	{
		Slug: "summary", Name: "Summarising", Repeats: 3,
		About:   "Boil a paragraph down to one sentence",
		Prompt:  "Summarise this in one sentence of no more than 25 words.\n\nThe town council has approved a plan to close the high street to cars on Saturdays from June to September. Shops will be allowed to put tables and displays on the road, and the council will run free buses from the edge of town. Some traders worry that customers who drive in from nearby villages will stop coming, so the council has agreed to review the scheme in October, using footfall figures and a survey of businesses, before deciding whether to make it permanent.",
		LookFor: "One sentence, 25 words or fewer, that keeps the main points: Saturday closures to cars for the summer, and a review in October before it's made permanent. Watch for details that aren't in the text.",
	},
	{
		Slug: "translation", Name: "Translation", Repeats: 3,
		About:   "One sentence into three languages",
		Prompt:  "Translate this into French, Spanish and German, each on its own line starting with the language's name:\n\nThe meeting has moved to Thursday at 3pm; please bring the latest sales figures.",
		LookFor: "Three natural translations, each labelled, with the day, time and \"sales figures\" all kept. If you speak one of the languages, check it reads naturally rather than word for word.",
	},
	{
		Slug: "long", Name: "Longer answer", Repeats: 3,
		About:   "A few hundred words, to compare speed over a longer reply",
		Prompt:  "Explain how a refrigerator keeps food cold, in about 300 words, for a curious 12-year-old.",
		LookFor: "A clear, accurate explanation (a fluid that takes heat away as it evaporates inside, and gives it out at the back) at about the right length and level. Tokens/s over a longer reply is a fairer measure of speed than a short one.",
	},
}

// builtinTestBySlug is the built-in test with this slug, or nil.
func builtinTestBySlug(slug string) *builtinTest {
	for i := range builtinTests {
		if builtinTests[i].Slug == slug {
			return &builtinTests[i]
		}
	}
	return nil
}

// builtinTestFor is the built-in test a test was run from (its prompt is
// the same), or nil, so its results page can say what to look for.
func builtinTestFor(prompt string) *builtinTest {
	for i := range builtinTests {
		if builtinTests[i].Prompt == prompt {
			return &builtinTests[i]
		}
	}
	return nil
}
