import Feature, { type FeatureProps } from './_private/Feature'

// pages.Props is in the app's types.d.ts (see the TypeScript docs); data is what index.ts load() returned
type PageProps = pages.Props & { data: { features: FeatureProps[]; now: string } }

export default function Home({ data, slot }: PageProps) {
  slot('title', 'PocketOne')
  return (
    <>
      <h1>PocketOne</h1>
      <p class="lead">Server-rendered TSX on PocketBase. Edit pb_pages/index.tsx and reload.</p>
      <div class="grid">
        {data.features.map((f) => (
          <Feature {...f} />
        ))}
      </div>
      <footer>
        Rendered at {data.now}. <a href="https://pocketone.dev/docs/extensions/pages">Pages docs</a>
      </footer>
    </>
  )
}
