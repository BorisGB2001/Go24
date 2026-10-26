// The Excalidraw editor for «Доски», bundled into go24 (no CDN): tools/excalidraw/build.sh puts it into
// internal/web/static/vendor/excalidraw; static/js/board.js mounts it.
import * as React from "react";
import { createRoot } from "react-dom/client";
import * as X from "@excalidraw/excalidraw";
import "@excalidraw/excalidraw/index.css";

window.Go24Excalidraw = { React, createRoot, ...X };
window.dispatchEvent(new Event("go24-excalidraw"));
