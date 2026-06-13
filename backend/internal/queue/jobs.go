package queue

// Job type identifiers. Add new ones here as async work is introduced.
const (
	// TypeContactNotify emails Alif about a new contact-form submission.
	TypeContactNotify = "contact.notify"
)

// ContactNotifyPayload is the body of a TypeContactNotify job.
type ContactNotifyPayload struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Body  string `json:"body"`
}
