/// <reference path="../types.d.ts" />
// views: how many times a story was listened to for at least 10 seconds (POST /view). The front page lists stories by it.
migrate((app) => {
  const stories = app.findCollectionByNameOrId('stories')
  stories.fields.add(new NumberField({ name: 'views', min: 0, onlyInt: true }))
  stories.addIndex('idx_stories_views', false, 'views', '')
  app.save(stories)
}, (app) => {
  const stories = app.findCollectionByNameOrId('stories')
  stories.removeIndex('idx_stories_views')
  stories.fields.removeByName('views')
  app.save(stories)
})
