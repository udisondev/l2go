// Чат SAY2 канала ALL (P3.11): валидация у владельца отправителя и рассылка
// CreatureSay в радиусе речи. Порт канона Mobius CT_0_Interlude @43ac8878
// (Say2.java, ChatGeneral.java, FloodProtector.ini — построчная сверка в
// реестре P3.11); всё исполняется свёрткой региона (single-writer), кадры —
// через res.Pushes, время — dt-тиками (D1).

package world

import (
	"strings"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

const (
	// chatSayMaxUnits — лимит текста реплики в UTF-16 code units (порт
	// Say2.java @43ac8878: >105 без item-link; «official client as 105»).
	chatSayMaxUnits = 105
	// chatSayRadius — радиус речи ALL, 3D-дистанция (порт ChatGeneral.java
	// @43ac8878: forEachVisibleObjectInRange(говорящий, Player, 1250);
	// World.java — calculateDistance3D). Меньше enter-радиуса известности
	// 3500 (replica.CanonJoinConfig) — «известность ∩ слышимость» выполняется
	// автоматически.
	chatSayRadius = 1250
	// chatSayCostMS — интервал спам-лимита: порт FloodProtector.ini
	// GlobalChatInterval=5 тиков (заголовок ini: «1 tick = 100ms»). Канон
	// применяет протектор к SHOUT/TRADE (ChatShout/ChatTrade:
	// canUseGlobalChat), ALL в каноне без защиты — перенос интервала на ALL
	// требует спека фазы 3 (запись-отклонение, реестр P3.11/F16); бакет
	// per-сущность с рождением=CAP эквивалентен канону per-коннект.
	chatSayCostMS = int64(500)
	// chatSayCapMS — бакет не копит сверх интервала (буст 1 — «only one
	// request per interval», без бurst-допуска).
	chatSayCapMS = int64(500)
)

// foldSay2 — реплика чата: валидация по канону → токен-бакет → рассылка
// CreatureSay живым игрокам в радиусе речи (кроме отправителя), эхо —
// последним кадром ветки (каноничный порядок пушей: рассылка, затем
// sendPacket себе — ChatGeneral.java). Мусорный тип и пустой текст —
// «packet hack» канона (Say2.java:114–129): ActionFailed + полный уход
// foldLogout — ConnClose не порождает LinkDead (шлюз: «закрытие инициировано
// миром»), без ухода сущность жила бы вечно (F1 реестра). Отправитель сам в
// радиусе (d=0), но пропускается — эхо отдельным кадром.
func foldSay2(tick Tick, st *State, ents []*Entity, ent *Entity, env *transport.Envelope, rules Rules, res *StepResult) {
	v, ok := protocol.NewSay2View(env.Payload)
	if !ok {
		st.DroppedFrames++
		return
	}
	if t := v.Type(); t < protocol.ChatGeneral || t > protocol.ChatMPCCRoom {
		pushActionFailed(res, ent)
		foldLogout(tick, st, ent, rules, res)
		return
	}
	text, _ := v.Text() // отказ невозможен после успешного NewSay2View
	if text == "" {
		pushActionFailed(res, ent)
		foldLogout(tick, st, ent, rules, res)
		return
	}
	// \b (0x08) — item-link канона (Say2.java parseAndPublishItem): предметов
	// в фазе 3 нет — дроп с метрикой.
	units, clean := v.TextMeasure()
	if units > chatSayMaxUnits || !clean || strings.ContainsRune(text, '\b') {
		// Канон отвечает SystemMessage keyboard-warning и рвёт item-link;
		// фаза 3 — дроп с метрикой по букве критерия приёмки, коннект жив
		// (записи-отклонения, реестр P3.11).
		st.ChatDropped++
		return
	}
	if t := v.Type(); t != protocol.ChatGeneral {
		// Не-ALL каналы — ignore со счётчиком (без лога на кадр: лог-DoS
		// валидными кадрами, прецедент спидхака); whisper-адресат не парсится.
		st.ChatIgnored++
		return
	}
	p := ent.Player
	if p.ChatBudget < chatSayCostMS {
		st.ChatFlooded++
		return
	}
	p.ChatBudget -= chatSayCostMS

	objID := int32(encode.ObjectIDBase + uint64(ent.ID))
	frame := make([]byte, protocol.CreatureSaySize(p.Rec.Name, text))
	protocol.WriteCreatureSay(frame, objID, protocol.ChatGeneral, p.Rec.Name, text)
	for _, e := range ents {
		if e.Player == nil || e.ID == ent.ID || leavingEntity(st, e.ID) {
			continue
		}
		if !withinSayRadius(ent.Pos, e.Pos) {
			continue
		}
		res.Pushes = append(res.Pushes, FramePush{Client: e.Player.ConnID, Frame: frame, Crypt: true})
	}
	res.Pushes = append(res.Pushes, FramePush{Client: p.ConnID, Frame: frame, Crypt: true})
}

// withinSayRadius — 3D-дистанция ≤ радиуса речи: сравнение квадратов в
// int64 (int32-квадрат переполняется на дельтах мира ±655360 — домен
// geo.InWorld), без sqrt. Позиции прод-домена всегда в сетке мира: суммы
// квадратов int64 не переполняются; литеральные края int32 (вне домена)
// не паникуют — wrap даёт ложный «вне радиуса», безопасно.
func withinSayRadius(a, b Position) bool {
	dx := int64(a.X) - int64(b.X)
	dy := int64(a.Y) - int64(b.Y)
	dz := int64(a.Z) - int64(b.Z)
	return dx*dx+dy*dy+dz*dz <= int64(chatSayRadius)*int64(chatSayRadius)
}
