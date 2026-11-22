/// <reference path="../types.d.ts" />
// samples: audio the dashboard's Story Time screen plays after Sample (the voice, the music). The dashboard only
// plays audio from its own origin, so each sample is saved here and played from /api/files/samples/<id>/<file>. Nobody
// can list them; a sample is viewable by its random id only. The app keeps the last 20.
migrate((app) => {
  const samples = new Collection({
    type: 'base',
    name: 'samples',
    listRule: null,
    viewRule: '',
    createRule: null,
    updateRule: null,
    deleteRule: null,
    fields: [
      { name: 'what', type: 'text', max: 40 },
      { name: 'audio', type: 'file', maxSelect: 1, maxSize: 31457280 },
      { name: 'created', type: 'autodate', onCreate: true },
    ],
    indexes: ['CREATE INDEX idx_samples_created ON samples (created)'],
  })
  app.save(samples)
}, (app) => {
  app.delete(app.findCollectionByNameOrId('samples'))
})
