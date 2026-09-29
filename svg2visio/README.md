# svg2vsdx

Convert SVG diagrams into **Microsoft Visio (`.vsdx`) files** made of native,
editable shapes, glued connectors and text. Pure Go, standard library only,
works fully offline.

The output opens in Visio and LibreOffice Draw, and imports as an editable
diagram into Miro (Starter/Business/Enterprise plans), Lucidchart and draw.io.

```sh
go build -o svg2vsdx ./cmd/svg2vsdx

./svg2vsdx events.svg                    # -> events.vsdx
./svg2vsdx -o out.vsdx events.svg
./svg2vsdx a.svg b.svg c.svg             # batch: a.vsdx b.vsdx c.vsdx
cat events.svg | ./svg2vsdx -o - - > events.vsdx
```

## What gets converted

**Graphviz SVG** (dot, protodot, anything with `class="graph|cluster|node|edge"`):

| SVG            | Visio                                                                 |
|----------------|-----------------------------------------------------------------------|
| `g.cluster`    | container shape with its label                                        |
| `g.node`       | shape (rectangle, ellipse, diamond, … from the outline), fill and border colors |
| node text      | shape text: bold/italic/underline kept, monospace columns aligned, record rows spaced exactly on the drawing's rows |
| `g.edge`       | dynamic connector **glued** to a connection point at the same spot as in the drawing (record port / row), color, dash and arrowheads kept, edge label as connector text |
| other text     | text shapes                                                           |

By default every node is written as a **Visio group**: the node box, each
colored cell (dark header bar, grey type column, enum/missing-type fills) and
the row texts are sub-shapes, so colors and styling match the SVG while the
node still moves and connects as one object. Connectors keep the SVG's
original curved path and are glued to the group. `-mode compact` writes one
plain shape per node instead (lighter, single fill color).

**Any other SVG** is mapped primitive by primitive: `rect`, `circle`,
`ellipse`, `polygon` and closed `path` become shapes (triangle, diamond,
pentagon/hexagon/octagon recognized); text on a shape becomes its label;
`line`/open `path` whose ends touch two shapes becomes a glued connector
(arrow if `marker-end`). Free-form curves, images and `<use>` have no native
equivalent and are reported as warnings.

## Flags

| flag | default | |
|---|---|---|
| `-o` | `<input>.vsdx` | output path, `-` for stdout (single input only) |
| `-mode` | `detailed` | `detailed` (node = group of colored cells) or `compact` (one plain shape per node) |
| `-scale` | `1` | page points per SVG unit |
| `-font-scale` | `1` | extra font size multiplier |
| `-mono-font` / `-sans-font` / `-serif-font` | Courier New / Arial / Times New Roman | fonts used |
| `-route` | `original` | connector path: `original` (as drawn in the SVG), `straight` or `curved` (app re-routes) |
| `-q` | off | no summary output |

## Layout

```
cmd/svg2vsdx       CLI
internal/svgdoc    SVG parser: transforms, paths, text runs, colors
internal/scene     diagram model + Graphviz / generic converters
internal/vsdx      .vsdx (OPC zip) writer: shapes, connection points, connectors, glue
testdata/          protodot sample: 150 nodes, 425 edges, 5 clusters
```

`go test ./...` converts the sample and checks the package: every part is
well-formed XML, all 425 connectors exist and both ends of each are glued
(850 `Connect` records).
