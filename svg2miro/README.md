# svg2miro

Convert an SVG diagram into **native, editable Miro items** — frames, shapes,
text and real connectors — via the Miro REST API v2. Written in Go, no
dependencies beyond the standard library.

Miro imports SVG only as a flat picture. `svg2miro` instead rebuilds the
diagram out of Miro objects you can recolor, move and re-route.

## What gets converted

**Graphviz SVG** (dot, protodot, anything with `class="graph|cluster|node|edge"`) is recognized:

| SVG                    | Miro                                                          |
|------------------------|---------------------------------------------------------------|
| `g.cluster`            | frame (title = cluster label, items are parented to it)       |
| `g.node`               | shape; outline → shape kind, fill/border colors kept          |
| node label text        | rich text in the shape (bold/italic/underline kept; monospace columns aligned) |
| `g.edge`               | connector between the two node shapes, attached at the *same point* (record port / row) as in the drawing, color, dash and arrowheads kept, edge label → caption |
| other text             | text items                                                    |

`-mode detailed` additionally emits every filled record cell as its own shape
(e.g. dark header bar, grey type column), trading ~6× more items for a
pixel-close look. Connectors still attach to the node container.

**Any other SVG** is mapped primitive by primitive: `rect`/`circle`/`ellipse`/
`polygon`/closed `path` → shapes (triangle, rhombus, hexagon, … detected),
text inside a shape becomes its label, `line`/open `path` whose two ends touch
two shapes → connector (arrow if `marker-end`). Free-form curves, images and
`<use>` have no native Miro equivalent and are reported as warnings.

## Usage

```sh
go build -o svg2miro ./cmd/svg2miro

# 1. look before you publish — no token needed
./svg2miro -preview preview.svg -plan plan.json diagram.svg

# 2. publish to an existing board …
export MIRO_TOKEN=...            # OAuth token with boards:write
./svg2miro -board uXjVKxxxxxx= diagram.svg

# … or to a new one
./svg2miro -new-board "events.proto" diagram.svg
```

Key flags (`-h` for all):

| flag | default | |
|---|---|---|
| `-mode` | `compact` | `compact` (one shape per node) or `detailed` (one shape per record cell) |
| `-scale` | `2` | board units per SVG unit |
| `-font-scale` | `0.85` | raise to `1.0` to make text rows line up exactly with record-row connector points |
| `-connector` | `curved` | `curved`, `straight`, `elbowed` |
| `-x`, `-y` | `0,0` | where the diagram's top-left lands on the board |
| `-mono-font` | `roboto_mono` | Miro `fontFamily` used for monospace labels |
| `-frames-as-parents` | `true` | parent items to the cluster frame containing them |
| `-bulk` | `true` | create shapes/text 20 per call via `/items/bulk` (falls back to single calls if a batch is rejected) |
| `-concurrency` | `4` | parallel requests for connectors |
| `-dry-run` / `-plan f.json` | | don't call Miro; record the exact API requests |

Rate limits (HTTP 429, honoring `Retry-After`) and 5xx are retried with backoff.
401/403/404 abort immediately. Other per-item failures are reported at the end
without stopping the run.

## Getting a token

Create an app at <https://miro.com/app/settings/user-profile/apps>, give it the
`boards:read` and `boards:write` scopes, install it to your team and copy the
access token. The board id is the part after `/board/` in the board URL.

## Layout

```
cmd/svg2miro        CLI
internal/svgdoc     SVG parser: transforms, paths, text runs, colors
internal/scene      backend-neutral diagram model + Graphviz / generic converters
internal/miro       REST client, payload builders, publisher, dry-run recorder
internal/preview    renders the scene as the board will look (SVG)
testdata/           protodot sample (150 nodes, 425 edges, 5 clusters)
```

`go test ./...` runs the converters on the sample and publishes it to a strict
in-process fake of the Miro API (validates every payload, simulates 429s and a
rejected bulk batch).
