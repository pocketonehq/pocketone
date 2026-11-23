// Runs on the server before index.tsx renders; what load() returns is the page's `data`.
import type { FeatureProps } from './_private/Feature'

export function load(ctx: pages.Context): { features: FeatureProps[]; now: string } {
  return {
    features: [
      { icon: '⚡', title: 'Server-rendered', text: 'Components render to HTML in PocketBase. No client runtime.' },
      { icon: '🧩', title: 'Components', text: 'Typed props, imported from _private/, with no build step.' },
      { icon: '🗄️', title: 'PocketBase inside', text: 'ctx.pb() reads your collections as the visitor.' },
    ],
    now: new Date().toISOString(),
  }
}
