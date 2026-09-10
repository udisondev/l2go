// Package l2client — headless-клиент Interlude 746: логин-флоу, game-хендшейк
// и стационарная фаза с детерминированным трафик-логом. Порядок флоу — канон
// L2J Mobius master CT_0_Interlude (43ac8878): login Init ← сервер, затем
// AuthGameGuard → GGAuth → RequestAuthLogin → LoginOk → ServerList → PlayOk;
// game — клиент говорит первым (ProtocolVersion открытым текстом), KeyPacket
// открытым текстом, дальше шифрование; порт семантики — udisondev/interlude
// pkg/l2client@34fe4c8. Конкурентная модель без общей мутации: хендшейк —
// синхронные стадии; стационарная фаза — горутина чтения сырых кадров и
// единственный цикл Run, владеющий криптодвижком, диспетчером и логом.
package l2client
