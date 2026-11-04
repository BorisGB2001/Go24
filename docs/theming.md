Русская версия: [ru/theming.md](ru/theming.md)

# Styling and Themes in go24

The appearance of go24 is configured entirely via CSS: colors, fonts, spacing, border radii, and icons. To customize it, you do not need to touch templates or code, and you do not need to rebuild the container image.

## How styles are organized

| File | Contents | When to change |
|---|---|---|
| `internal/web/static/css/tokens.css` | **Design tokens**: all colors (including dark theme), fonts, sizes, spacing, shadows, status colors, avatar parameters | Default product look and feel |
| `internal/web/static/css/icons.css` | Mapping of icon to file: `.i-check { --icon: url(...) }` | Adding or replacing default icons |
| `internal/web/static/css/app.css` | Components: menus, cards, tables, buttons, kanban. **There are no specific colors or sizes here**, only variables | Only when modifying layout |
| `deploy/theme/theme.css` | **Theme of a specific installation**, loaded last and overrides everything above | Custom styling without rebuild |

## Custom theme without rebuild

1. Copy `deploy/theme/theme.css.example` to `deploy/theme/theme.css`.
2. Override the necessary variables from `tokens.css`:

```css
:root {
  --accent: #7c3aed;
  --side-bg: #2b1a47;
  --radius: 6px;
}
```

3. Refresh the page. go24 checks for the existence of `theme.css` on every request, so a restart is not needed.

The `deploy/theme/` directory is mounted into the container as `/theme` (the `GO24_THEME_DIR` environment variable) and is accessible in the browser at `/theme/...`. Therefore, you can place fonts, logos, and icons alongside it and reference them from `theme.css`.

## Icons

In markup, an icon is an empty element `<i class="i i-NAME">`. The image is defined in CSS via the `--icon` variable and is used as a mask that is colored with the text color. Therefore, **any single-color SVG** is suitable: Tabler, Font Awesome, Material Symbols, or custom artwork. The color and size of the icon come from CSS: `color` and `--icon-size`.

Replacing a single icon in the theme:

```css
.i-checkbox { --icon: url("/theme/icons/my-tasks.svg"); }
```

Replacing the entire icon set: place your SVGs in `deploy/theme/icons/` and override all `.i-*` classes in `theme.css` (their list is in `icons.css`).

The standard set is Tabler Icons outline (MIT license, `internal/web/static/icons/LICENSE`). New icons are sourced from the same collection: put the SVG in `static/icons/` and add the line to `icons.css`. The `TestIcons` test verifies that each icon from the templates exists and that each SVG has a corresponding class in `icons.css`.

## Variables (main)

- `--accent`, `--accent-soft`, `--on-accent` — brand color, its lighter variant, and text color on it.
- `--side-bg`, `--side-bg-hover`, `--side-text`, `--side-text-active` — left sidebar menu.
- `--bg`, `--card`, `--text`, `--muted`, `--line`, `--hover` — background, cards, text, muted text, lines, hover state.
- `--ok`, `--warn`, `--danger`, `--violet` and their `*-soft` variants — semantic colors.
- `--st-<status>-bg` and `--st-<status>-fg` — task status colors.
- `--ava-saturation`, `--ava-lightness`, `--ava-text` — avatars. Hue is calculated from the name, the rest is configured here.
- `--font`, `--font-size*`, `--radius*`, `--gap`, `--pad`, `--content-pad`, `--shadow`, `--icon-size`, `--side-width`.

Dark theme is defined in `tokens.css` with a `@media (prefers-color-scheme: dark)` block. In a custom theme, it can be overridden in the same way.
