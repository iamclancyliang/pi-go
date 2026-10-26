// Resolves "@earendil-works/pi-*" to the TypeScript sources of the pinned
// tree in PI_PIN: the archive has no build output, and the checkout's own
// node_modules links point at whatever revision it has checked out.
import { existsSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const packages = { "pi-tui": "tui", "pi-ai": "ai", "pi-agent-core": "agent", "pi-coding-agent": "coding-agent" };

export async function resolve(specifier, context, next) {
	const m = /^@earendil-works\/(pi-[a-z-]+)(\/.*)?$/.exec(specifier);
	if (m && packages[m[1]]) {
		const src = join(process.env.PI_PIN, "packages", packages[m[1]], "src");
		const sub = m[2] ? m[2].slice(1) : "index";
		for (const candidate of [`${sub}.ts`, join(sub, "index.ts")]) {
			const file = join(src, candidate);
			if (existsSync(file)) return { url: pathToFileURL(file).href, shortCircuit: true };
		}
	}
	return next(specifier, context);
}
