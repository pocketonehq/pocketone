/// <reference path="../types.d.ts" />
// log: what happened while a story was made, step by step (start, done, timeout, error, retry), with how long each took;
// hidden, so superusers read it in the dashboard. attempts: how many times the story was tried (a stalled or failed
// attempt is retried once, resuming from the steps it saved).
migrate((app) => {
  const stories = app.findCollectionByNameOrId('stories')
  stories.fields.add(new JSONField({ name: 'log', hidden: true, maxSize: 200000 }))
  stories.fields.add(new NumberField({ name: 'attempts', min: 0, onlyInt: true }))
  app.save(stories)
}, (app) => {
  const stories = app.findCollectionByNameOrId('stories')
  stories.fields.removeByName('log')
  stories.fields.removeByName('attempts')
  app.save(stories)
})
