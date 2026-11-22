/// <reference path="../types.d.ts" />
// settings: the app's tuning a superuser saves on /admin (key "tuning"; the models, the narrator's voice, the prompts,
// the picture and music additions, the daily budget), as JSON on top of the defaults in pb_pages/_private/story.ts.
// Only the app and superusers read or write it.
migrate((app) => {
  const settings = new Collection({
    type: 'base',
    name: 'settings',
    listRule: null,
    viewRule: null,
    createRule: null,
    updateRule: null,
    deleteRule: null,
    fields: [
      { name: 'key', type: 'text', required: true, max: 100 },
      { name: 'value', type: 'json', maxSize: 200000 },
      { name: 'updated', type: 'autodate', onCreate: true, onUpdate: true },
    ],
    indexes: ['CREATE UNIQUE INDEX idx_settings_key ON settings (key)'],
  })
  app.save(settings)
}, (app) => {
  app.delete(app.findCollectionByNameOrId('settings'))
})
