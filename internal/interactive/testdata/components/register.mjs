// Loads hooks.mjs, which resolves Pi's workspace packages to the pinned
// sources rather than their unbuilt dist directories.
import { register } from "node:module";
register("./hooks.mjs", import.meta.url);
