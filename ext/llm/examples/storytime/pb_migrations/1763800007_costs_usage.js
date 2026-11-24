/// <reference path="../types.d.ts" />
// usage: what each model call used (tokens in and out, seconds of audio), as the llm contracts report it, so the
// settings screen can tell what a story's steps use and price every model for that same work.
migrate((app) => {
  const costs = app.findCollectionByNameOrId('costs')
  costs.fields.add(new JSONField({ name: 'usage', maxSize: 5000 }))
  app.save(costs)
}, (app) => {
  const costs = app.findCollectionByNameOrId('costs')
  costs.fields.removeByName('usage')
  app.save(costs)
})
