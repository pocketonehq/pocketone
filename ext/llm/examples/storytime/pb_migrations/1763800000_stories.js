/// <reference path="../types.d.ts" />
// The stories collection: one record per story, written by the app as it makes the story (status and progress), then
// its pictures, narration and song as files, and the story itself with its timings as JSON. Anyone can list and
// view stories (so a link can be shared); only the app writes them.
migrate((app) => {
  const stories = new Collection({
    type: 'base',
    name: 'stories',
    listRule: '',
    viewRule: '',
    createRule: null,
    updateRule: null,
    deleteRule: null,
    fields: [
      { name: 'prompt', type: 'text', required: true, max: 500 },
      { name: 'title', type: 'text', max: 200 },
      { name: 'minutes', type: 'number', required: true, min: 1, max: 10, onlyInt: true },
      { name: 'picture_count', type: 'number', required: true, min: 1, max: 12, onlyInt: true },
      { name: 'status', type: 'select', required: true, maxSelect: 1, values: ['queued', 'writing', 'making', 'done', 'failed'] },
      { name: 'progress', type: 'text', max: 500 },
      { name: 'story', type: 'json', maxSize: 2000000 },
      { name: 'pictures', type: 'file', maxSelect: 10, maxSize: 20971520, thumbs: ['480x270'] },
      { name: 'narration', type: 'file', maxSelect: 1, maxSize: 52428800 },
      { name: 'music', type: 'file', maxSelect: 1, maxSize: 31457280 },
      { name: 'cost', type: 'number' },
      { name: 'error', type: 'text', max: 2000 },
      { name: 'created', type: 'autodate', onCreate: true },
      { name: 'updated', type: 'autodate', onCreate: true, onUpdate: true },
    ],
    indexes: ['CREATE INDEX idx_stories_created ON stories (created)'],
  })
  app.save(stories)
}, (app) => {
  app.delete(app.findCollectionByNameOrId('stories'))
})
