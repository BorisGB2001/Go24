package sites

import "strings"

// TemplatePage is a page of a ready site or a ready page: Key — its address (the home page of a site is always
// «index») and the key of its title, Blocks — its blocks, each «type» or «type:variant»; the texts of a block are
// <scope>.<variant>.<field> (the variant is the type when not given), else the texts of a new block of the type.
type TemplatePage struct {
	Key    string
	Blocks []string
}

// Template is a ready site of the gallery «Создать сайт»: its title and description are locale keys
// site.tpl.<key> and site.tpl.<key>.desc, its pages' titles site.tpl.<key>.page.<page>, the texts of the blocks
// site.tpl.<key>.<variant>.<field>. The first page is the home page.
type Template struct {
	Key, Icon string
	Pages     []TemplatePage
}

// SiteTemplates is the gallery of sites, in the order it is shown; the first one is chosen by default.
var SiteTemplates = []Template{
	{"company", "users-group", []TemplatePage{
		{"index", []string{"menu", "cover", "textimage", "features", "contacts", "footer"}},
		{"about", []string{"menu", "title", "text", "textimage:team", "footer"}},
		{"contacts", []string{"menu", "contacts", "form", "footer"}},
	}},
	{"services", "tool", []TemplatePage{
		{"index", []string{"menu", "cover", "features", "textimage", "button", "form", "footer"}},
		{"prices", []string{"menu", "title", "features:prices", "button", "footer"}},
	}},
	{"event", "calendar-event", []TemplatePage{
		{"index", []string{"menu", "cover", "title", "text", "features:program", "contacts", "form", "footer"}},
	}},
	{"blank", "file-plus", []TemplatePage{{"index", nil}}},
}

// PageTemplates are the ready pages of «Новая страница», in the order they are shown; except the empty one they
// get the menu and the footer of the site's home page, as a page made from a template does. Their titles
// are site.ptpl.<key>, the texts site.ptpl.<key>.<variant>.<field>.
var PageTemplates = []TemplatePage{
	{"blank", nil},
	{"text", []string{"title", "text"}},
	{"about", []string{"title", "textimage", "features"}},
	{"services", []string{"title", "features", "button"}},
	{"contacts", []string{"contacts", "form"}},
}

// SiteTemplate finds a site template; an unknown key is the first one.
func SiteTemplate(key string) Template {
	for _, t := range SiteTemplates {
		if t.Key == key {
			return t
		}
	}
	return SiteTemplates[0]
}

// PageTemplate finds a page template; an unknown key is the empty page.
func PageTemplate(key string) TemplatePage {
	for _, t := range PageTemplates {
		if t.Key == key {
			return t
		}
	}
	return PageTemplates[0]
}

// Texts gives the text of a field of a block of a template: by its variant, else by its type; ok false — none.
type Texts func(typ, variant, field string) (string, bool)

// Build makes the blocks of a ready page. The texts may name {site} — the address of the site (links to its pages
// are /site/{site}/<page>), {name} — the name of the site, {form} — an anchor of the page's first form block,
// {#variant} — an anchor of the page's block of the variant.
func Build(specs []string, text Texts, site, name string) []Block {
	out, anchors := build(specs, text)
	form := ""
	if f := firstForm(out); f != "" {
		form = "#b-" + f
	}
	fill(out, append(anchors, "{site}", site, "{name}", name, "{form}", form))
	return out
}

// BuildSite makes the pages of a site of the gallery, titled by title(page key); {form} there is a link to the first
// form block of the site, from any of its pages.
func BuildSite(t Template, text Texts, title func(page string) string, site, name string) []NewPage {
	out := make([]NewPage, 0, len(t.Pages))
	anchors := make([][]string, len(t.Pages))
	form := ""
	for i, p := range t.Pages {
		var blocks []Block
		blocks, anchors[i] = build(p.Blocks, text)
		if f := firstForm(blocks); f != "" && form == "" {
			form = "/site/" + site
			if i > 0 {
				form += "/" + p.Key
			}
			form += "#b-" + f
		}
		out = append(out, NewPage{Slug: p.Key, Title: title(p.Key), Blocks: blocks})
	}
	for i, p := range out {
		fill(p.Blocks, append(anchors[i], "{site}", site, "{name}", name, "{form}", form))
	}
	return out
}

// build makes the blocks with their texts as they are, and the pairs «{#variant}» → its anchor.
func build(specs []string, text Texts) ([]Block, []string) {
	out := make([]Block, 0, len(specs))
	var anchors []string
	for _, spec := range specs {
		typ, variant, _ := strings.Cut(spec, ":")
		if variant == "" {
			variant = typ
		}
		t, ok := TypeOf(typ)
		if !ok {
			continue
		}
		b := Block{ID: newID(), Type: typ, Data: map[string]string{}}
		for _, f := range t.Fields {
			if v, ok := text(typ, variant, f.Name); ok {
				b.Data[f.Name] = v
			}
		}
		anchors = append(anchors, "{#"+variant+"}", "#b-"+b.ID)
		out = append(out, b)
	}
	return out, anchors
}

func firstForm(list []Block) string {
	for _, b := range list {
		if b.Type == "form" {
			return b.ID
		}
	}
	return ""
}

func fill(list []Block, pairs []string) {
	r := strings.NewReplacer(pairs...)
	for _, b := range list {
		for k, v := range b.Data {
			b.Data[k] = r.Replace(v)
		}
	}
}

// Chrome are copies of the first menu and the last footer of a page, when it has them: the frame of a new page made
// from a template.
func Chrome(list []Block) (menu, footer []Block) {
	for _, b := range list {
		if b.Type == "menu" {
			menu = []Block{copyBlock(b)}
			break
		}
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Type == "footer" {
			footer = []Block{copyBlock(list[i])}
			break
		}
	}
	return menu, footer
}

func copyBlock(b Block) Block {
	d := make(map[string]string, len(b.Data))
	for k, v := range b.Data {
		d[k] = v
	}
	b.ID, b.Data = newID(), d
	return b
}
