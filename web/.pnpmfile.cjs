// TypeScript 7 (the native compiler) ships no JavaScript compiler API, which
// typescript-eslint and openapi-typescript need. Give those tools their own
// TypeScript 6 instead of the project's TypeScript 7; the app itself is
// type-checked by TypeScript 7. Pin and reason: docs/VERSIONS.md.
const TS6 = 'npm:typescript@6.0.3'
const needsTs6 = (name) =>
  name === 'typescript-eslint' ||
  name.startsWith('@typescript-eslint/') ||
  name === 'ts-api-utils' ||
  name === 'openapi-typescript'

module.exports = {
  hooks: {
    readPackage(pkg) {
      if (needsTs6(pkg.name)) {
        if (pkg.peerDependencies) delete pkg.peerDependencies.typescript
        if (pkg.peerDependenciesMeta) delete pkg.peerDependenciesMeta.typescript
        pkg.dependencies = { ...pkg.dependencies, typescript: TS6 }
      }
      // geist declares next as a peer for its next/font helpers; dbairlock only
      // uses the font files, so do not pull in Next.js.
      if (pkg.name === 'geist' && pkg.peerDependencies) {
        delete pkg.peerDependencies.next
      }
      return pkg
    },
  },
}
