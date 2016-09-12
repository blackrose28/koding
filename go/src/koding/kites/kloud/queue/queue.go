package queue

import (
	"time"

	"github.com/koding/logging"
	"golang.org/x/net/context"

	"koding/db/mongodb/modelhelper"
	"koding/kites/kloud/machinestate"
	"koding/kites/kloud/provider/aws"
	"koding/kites/kloud/provider/solo/koding"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type Queue struct {
	AwsProvider *awsprovider.Provider
	Log         logging.Logger
}

// RunChecker runs the checker for Koding and AWS providers every given
// interval time. It fetches a single document.
func (q *Queue) RunCheckers(interval time.Duration) {
	q.Log.Debug("queue started with interval %s", interval)

	if q.AwsProvider == nil {
		q.Log.Warning("not running cleaner queue for aws koding provider")
	}

	for range time.Tick(interval) {
		// do not block the next tick
		go q.CheckAWS()
	}
}

// Fetch provider fetches the machine and populates the fields for the given
// provider.
func (q *Queue) FetchProvider(provider string, machine interface{}) error {
	query := func(c *mgo.Collection) error {
		// check only machines that:
		// 1. belongs to the given provider
		// 2. are running
		// 3. are not always on machines
		// 3. are not assigned to anyone yet (unlocked)
		// 4. are not picked up by others yet recently in last 30 seconds
		//
		// The $ne is used to catch documents whose field is not true including
		// that do not contain that particular field
		egligibleMachines := bson.M{
			"provider":            provider,
			"status.state":        machinestate.Running.String(),
			"meta.alwaysOn":       bson.M{"$ne": true},
			"assignee.inProgress": bson.M{"$ne": true},
			"assignee.assignedAt": bson.M{"$lt": time.Now().UTC().Add(-time.Second * 30)},
		}

		// update so we don't pick up recent things
		update := mgo.Change{
			Update: bson.M{
				"$set": bson.M{
					"assignee.assignedAt": time.Now().UTC(),
				},
			},
		}

		// We sort according to the latest assignment date, which let's us pick
		// always the oldest one instead of random/first. Returning an error
		// means there is no document that matches our criteria.
		// err := c.Find(egligibleMachines).Sort("assignee.assignedAt").One(&machine)
		_, err := c.Find(egligibleMachines).Sort("assignee.assignedAt").Apply(update, machine)
		if err != nil {
			return err
		}

		return nil
	}

	return q.AwsProvider.DB.Run("jMachines", query)
}

// StopIfKlientIsMissing will stop the current Machine X minutes after
// the `assignee.klientMissingAt` value. If the value does not exist in
// the databse, it will write it and return.
//
// Therefor, this method is expected be called as often as needed,
// and will shutdown the Machine if klient has been missing for too long.
func (q *Queue) StopIfKlientIsMissing(ctx context.Context, m *koding.Machine) error {
	// If this is the first time Klient has been found missing,
	// set the missingat time and return
	if m.Assignee.KlientMissingAt.IsZero() {
		m.Log.Debug("Klient has been reported missing, recording this as the first time it went missing")

		return m.Session.DB.Run("jMachines", func(c *mgo.Collection) error {
			return c.UpdateId(
				m.ObjectId,
				bson.M{"$set": bson.M{"assignee.klientMissingAt": time.Now().UTC()}},
			)
		})
	}

	// If the klient has been missing less than X minutes, don't stop
	if time.Since(m.Assignee.KlientMissingAt) < time.Minute*20 {
		return nil
	}

	// lock so it doesn't interfere with others.
	err := m.Lock()

	defer func(m *koding.Machine) {
		err := m.Unlock()
		if err != nil {
			m.Log.Error("Defer Error: Unlocking machine failed, %s", err.Error())
		}
	}(m)

	// Check for a Lock error
	if err != nil {
		return err
	}

	// Clear the klientMissingAt field, or we risk Stopping the user's
	// machine next time they run it, without waiting the proper X minute
	// timeout.
	defer func(m *koding.Machine) {
		if !m.Assignee.KlientMissingAt.IsZero() {
			err := modelhelper.UnsetKlientMissingAt(m.ObjectId)
			if err != nil {
				m.Log.Error("Defer Error: Call to klientIsNotMissing failed, %s", err.Error())
			}
		}
	}(m)

	// Hasta la vista, baby!
	m.Log.Info("======> STOP started (missing klient) <======, username:%s", m.Credential)
	if err := m.Stop(ctx); err != nil {
		m.Log.Info("======> STOP failed (missing klient: %s) <======", err)
		return err
	}
	m.Log.Info("======> STOP finished (missing klient) <======, username:%s", m.Credential)

	return nil
}
