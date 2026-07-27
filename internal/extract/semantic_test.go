package extract

import (
	"strings"
	"testing"
)

func TestSimplifyDOM(t *testing.T) {
	rawHTML := `<!DOCTYPE html>
<html>
<head>
    <title>Test Page</title>
    <style>body { background: red; }</style>
    <script>console.log("secret script");</script>
</head>
<body class="main-body" style="color: black;" onclick="doSomething()">
    <div id="header">
        <h1>Welcome to Test Page</h1>
        <svg><path d="M0 0h10v10H0z"/></svg>
    </div>
    <form id="search-form" action="/search" method="GET">
        <input type="text" name="q" placeholder="Search items..." class="input-field" style="width: 100%;" />
        <button type="submit" class="btn btn-primary" aria-label="Submit Search">Search</button>
    </form>
    <script src="bundle.js"></script>
</body>
</html>`

	simplified, err := SimplifyDOM(rawHTML)
	if err != nil {
		t.Fatalf("SimplifyDOM failed: %v", err)
	}

	// Should remove script and style tags and contents
	if strings.Contains(simplified, "secret script") {
		t.Errorf("expected script content to be removed, got: %s", simplified)
	}
	if strings.Contains(simplified, "background: red") {
		t.Errorf("expected style tag content to be removed, got: %s", simplified)
	}

	// Should remove inline style and event handler attributes
	if strings.Contains(simplified, `style="color: black;"`) || strings.Contains(simplified, `onclick="doSomething()"`) {
		t.Errorf("expected style/onclick attributes to be stripped, got: %s", simplified)
	}

	// Should retain id, class, name, placeholder, type, action, method, aria-label attributes
	if !strings.Contains(simplified, `id="search-form"`) {
		t.Errorf("expected id='search-form' to be preserved, got: %s", simplified)
	}
	if !strings.Contains(simplified, `placeholder="Search items..."`) {
		t.Errorf("expected placeholder attribute to be preserved, got: %s", simplified)
	}
	if !strings.Contains(simplified, `aria-label="Submit Search"`) {
		t.Errorf("expected aria-label to be preserved, got: %s", simplified)
	}
}

func TestCleanSelectorResponse(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"```css\n#search-input\n```", "#search-input"},
		{"`button.submit`", "button.submit"},
		{`"//input[@name='q']"`, "//input[@name='q']"},
		{"  input[type='text']  ", "input[type='text']"},
		{"```xpath\n//div[@id='content']//a[1]\n```", "//div[@id='content']//a[1]"},
	}

	for _, tt := range tests {
		actual := CleanSelectorResponse(tt.input)
		if actual != tt.expected {
			t.Errorf("CleanSelectorResponse(%q) = %q; expected %q", tt.input, actual, tt.expected)
		}
	}
}
