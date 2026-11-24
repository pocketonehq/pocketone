/// <reference path="../types.d.ts" />
// setup_files: what the dashboard's Set up steps make that's too big for $kv (step 3's reference picture). Superusers
// only, shown with their file token.
migrate((app) => {
  const files = new Collection({
    type: 'base',
    name: 'setup_files',
    listRule: null,
    viewRule: null,
    createRule: null,
    updateRule: null,
    deleteRule: null,
    fields: [
      { name: 'step', type: 'text', max: 40 },
      { name: 'file', type: 'file', maxSelect: 1, maxSize: 31457280, protected: true },
      { name: 'created', type: 'autodate', onCreate: true },
    ],
  })
  app.save(files)
}, (app) => {
  app.delete(app.findCollectionByNameOrId('setup_files'))
})
